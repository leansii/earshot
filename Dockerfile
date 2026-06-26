# SPDX-License-Identifier: Apache-2.0
# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary.
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/earshot ./cmd/earshot

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# Migrations are read from disk by `earshot migrate --dir migrations`.
COPY --from=build /src/migrations /app/migrations
COPY --from=build /out/earshot /usr/local/bin/earshot
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["earshot"]
CMD ["serve"]
