// SPDX-License-Identifier: Apache-2.0

// Package scrub redacts obvious secrets and PII from feedback before it can
// reach a (possibly public) GitHub issue. It is intentionally an explicit,
// auditable pipeline stage — not a best-effort afterthought.
//
// This is a pragmatic first line of defense, not a guarantee. It targets
// high-confidence patterns (emails, API tokens, private keys); advanced PII
// detection is tracked as a follow-up.
package scrub

import (
	"regexp"
	"strings"
)

type rule struct {
	name string
	re   *regexp.Regexp
}

// Order matters: more specific/secret patterns run before broad ones.
var rules = []rule{
	{"private-key-block", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"github-pat", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`)},
	{"github-fine-grained-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"openai-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"bearer-token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]{16,}\b`)},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)},
	{"credit-card", regexp.MustCompile(`\b(?:\d[ -]*?){13,16}\b`)},
}

// Report describes what a scrub pass removed, for auditability.
type Report struct {
	// Counts maps rule name -> number of redactions.
	Counts map[string]int
}

// Total returns the number of redactions across all rules.
func (r Report) Total() int {
	n := 0
	for _, c := range r.Counts {
		n += c
	}
	return n
}

// Text redacts secrets/PII in s and returns the cleaned string plus a report.
// Each match is replaced with a typed placeholder like [REDACTED:email].
func Text(s string) (string, Report) {
	rep := Report{Counts: map[string]int{}}
	for _, ru := range rules {
		s = ru.re.ReplaceAllStringFunc(s, func(string) string {
			rep.Counts[ru.name]++
			return "[REDACTED:" + ru.name + "]"
		})
	}
	return strings.TrimSpace(s), rep
}
