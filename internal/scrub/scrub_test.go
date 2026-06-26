// SPDX-License-Identifier: Apache-2.0

package scrub

import (
	"strings"
	"testing"
)

func TestText_RedactsSecretsAndPII(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantTag string
	}{
		{"email", "contact me at jane.doe@example.com please", "email"},
		{"github classic pat", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789AB", "github-pat"},
		{"aws key", "key AKIAIOSFODNN7EXAMPLE here", "aws-access-key"},
		{"openai key", "use sk-abcdefghijklmnopqrstuvwxyz012345 now", "openai-key"},
		{"bearer", "Authorization: Bearer abcdefghijklmnop1234", "bearer-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rep := Text(tc.in)
			if strings.Contains(got, "@example.com") && tc.wantTag == "email" {
				t.Fatalf("email not redacted: %q", got)
			}
			if rep.Counts[tc.wantTag] == 0 {
				t.Fatalf("expected a %q redaction, got report %+v (out=%q)", tc.wantTag, rep.Counts, got)
			}
			if !strings.Contains(got, "[REDACTED:"+tc.wantTag+"]") {
				t.Fatalf("expected placeholder for %q in %q", tc.wantTag, got)
			}
		})
	}
}

func TestText_PrivateKeyBlock(t *testing.T) {
	in := "oops\n-----BEGIN RSA PRIVATE KEY-----\nMIIE...secret...\n-----END RSA PRIVATE KEY-----\nbye"
	got, rep := Text(in)
	if rep.Counts["private-key-block"] != 1 {
		t.Fatalf("private key not redacted: %+v", rep.Counts)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("private key body leaked: %q", got)
	}
}

func TestText_CleanInputUnchanged(t *testing.T) {
	in := "The login button does nothing on the settings page."
	got, rep := Text(in)
	if got != in {
		t.Fatalf("clean text was altered: %q", got)
	}
	if rep.Total() != 0 {
		t.Fatalf("expected zero redactions, got %d", rep.Total())
	}
}
