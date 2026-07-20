<!-- SPDX-License-Identifier: Apache-2.0 -->
# Deploying Earshot on GCP (Neon + Cloud Run + scheduled drain)

Cheap-by-default topology: the database is Neon (serverless Postgres,
scale-to-zero), the ingest API is a request-billed Cloud Run service, and the
worker is **not** an always-on process — Cloud Scheduler periodically launches
a Cloud Run Job that runs `earshot worker --drain`, which empties the River
queue and exits. Between runs nothing is billed and Neon's compute can
suspend.

Trade-off: feedback is processed with a delay of up to one schedule interval.
Triage is not latency-sensitive, so the default below is 5 minutes — also long
enough to let Neon autosuspend (its default idle timeout is 5 minutes) instead
of being kept awake by the drain runs themselves.

## Prerequisites

- A Neon project (Postgres 16+; pgvector is available out of the box —
  migrations run `CREATE EXTENSION IF NOT EXISTS vector`).
- Use the **pooled** connection string (`...-pooler.<region>.neon.tech`) with
  `sslmode=require`: Cloud Run can run several instances, and the direct
  connection limit on small Neon computes is low.

```bash
PROJECT=<gcp-project>
REGION=us-central1   # free-tier region for Cloud Run
IMG=$REGION-docker.pkg.dev/$PROJECT/earshot/earshot:latest

gcloud config set project $PROJECT
gcloud services enable run.googleapis.com cloudscheduler.googleapis.com \
  artifactregistry.googleapis.com secretmanager.googleapis.com cloudbuild.googleapis.com
```

## 1. Secrets

```bash
# Neon pooled connection string, e.g.
# postgres://earshot:<password>@ep-xxx-pooler.us-east-2.aws.neon.tech/earshot?sslmode=require
printf '%s' "$NEON_DATABASE_URL" | gcloud secrets create earshot-database-url --data-file=-

# LLM API keys — mounted as files (EARSHOT_*_API_KEY_FILE is read lazily per
# call, so rotating a secret needs no redeploy). DeepSeek is the primary model;
# the Gemini key makes gemini:gemini-2.5-flash-lite a config-only switch.
printf '%s' "$DEEPSEEK_API_KEY" | gcloud secrets create earshot-deepseek-key --data-file=-
printf '%s' "$GEMINI_API_KEY"   | gcloud secrets create earshot-gemini-key --data-file=-
```

## 2. Runtime service account

```bash
gcloud iam service-accounts create earshot-run
SA=earshot-run@$PROJECT.iam.gserviceaccount.com
for s in earshot-database-url earshot-deepseek-key earshot-gemini-key; do
  gcloud secrets add-iam-policy-binding $s \
    --member=serviceAccount:$SA --role=roles/secretmanager.secretAccessor
done
```

## 3. Build the image

```bash
gcloud artifacts repositories create earshot --repository-format=docker --location=$REGION
gcloud builds submit --tag $IMG
```

## 4. Migrations (one-shot job, rerun on schema changes)

```bash
gcloud run jobs create earshot-migrate --image=$IMG --command=earshot --args=migrate \
  --region=$REGION --service-account=$SA \
  --set-secrets=EARSHOT_DATABASE_URL=earshot-database-url:latest
gcloud run jobs execute earshot-migrate --region=$REGION --wait
```

## 5. Ingest API (request-billed, scales to zero)

```bash
gcloud run deploy earshot-api --image=$IMG --command=earshot --args=serve \
  --region=$REGION --service-account=$SA \
  --cpu=1 --memory=512Mi --min-instances=0 --max-instances=3 \
  --set-env-vars=EARSHOT_QUEUE=river,EARSHOT_LLM_MODEL=deepseek:deepseek-v4-flash,EARSHOT_EMBED_MODEL=fake:hash-256,EARSHOT_DEEPSEEK_API_KEY_FILE=/secrets/deepseek/key,EARSHOT_GEMINI_API_KEY_FILE=/secrets/gemini/key \
  --set-secrets=EARSHOT_DATABASE_URL=earshot-database-url:latest,/secrets/deepseek/key=earshot-deepseek-key:latest,/secrets/gemini/key=earshot-gemini-key:latest \
  --allow-unauthenticated
```

## 6. Drain worker (Cloud Run Job)

```bash
gcloud run jobs create earshot-worker --image=$IMG \
  --command=earshot --args=worker,--drain \
  --region=$REGION --service-account=$SA \
  --cpu=1 --memory=512Mi --task-timeout=10m --max-retries=0 \
  --set-env-vars=EARSHOT_QUEUE=river,EARSHOT_LLM_MODEL=deepseek:deepseek-v4-flash,EARSHOT_EMBED_MODEL=fake:hash-256,EARSHOT_DEEPSEEK_API_KEY_FILE=/secrets/deepseek/key,EARSHOT_GEMINI_API_KEY_FILE=/secrets/gemini/key \
  --set-secrets=EARSHOT_DATABASE_URL=earshot-database-url:latest,/secrets/deepseek/key=earshot-deepseek-key:latest,/secrets/gemini/key=earshot-gemini-key:latest

# Smoke test:
gcloud run jobs execute earshot-worker --region=$REGION --wait
```

`--max-retries=0` because the schedule itself is the retry: a failed drain run
leaves the jobs in the queue and the next tick picks them up. River-level
retries of individual jobs are unaffected.

## 7. Schedule the drain

```bash
# Scheduler needs its own identity that is allowed to run the job.
gcloud iam service-accounts create earshot-scheduler
SCHED_SA=earshot-scheduler@$PROJECT.iam.gserviceaccount.com
gcloud run jobs add-iam-policy-binding earshot-worker --region=$REGION \
  --member=serviceAccount:$SCHED_SA --role=roles/run.invoker

gcloud scheduler jobs create http earshot-drain \
  --location=$REGION --schedule='*/5 * * * *' \
  --uri="https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/jobs/earshot-worker:run" \
  --http-method=POST \
  --oauth-service-account-email=$SCHED_SA
```

Tune `--schedule`: shorter intervals mean fresher triage but keep Neon's
compute awake more of the time (its cost is the dominant variable). `*/5` is a
good default; `*/2` if you want snappier processing and accept ~$20/mo for
Neon.

## Switching the LLM

Both keys are mounted, so switching model is a config-only change on the API
service and the worker job — no rebuild:

```bash
for target in "services update earshot-api" "jobs update earshot-worker"; do
  gcloud run $target --region=$REGION \
    --update-env-vars=EARSHOT_LLM_MODEL=gemini:gemini-2.5-flash-lite
done
```

## 8. Seed a project

Run locally against Neon (the CLI talks straight to the database):

```bash
EARSHOT_DATABASE_URL="$NEON_DATABASE_URL" \
  go run ./cmd/earshot seed-project --slug myapp --owner <org> --repo <repo> \
  --token-file ~/pat.txt
```

Prints the `esk_…` app key once — store it in your app's secret store.
