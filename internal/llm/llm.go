// SPDX-License-Identifier: Apache-2.0

// Package llm is a provider-agnostic interface for classifying feedback.
// Models are addressed as "provider:model" (e.g. "anthropic:claude-haiku-4-5",
// "fake:classifier-v1"), mirroring the design Earshot inherits from Vouch.
//
// Security note: feedback bodies are untrusted and a prompt-injection vector.
// Implementations MUST treat the body strictly as data — fixed system
// instructions, the body fenced inside an explicit delimiter, no tools exposed
// to the model, and no secrets placed in the context window.
package llm

import (
	"context"
	"fmt"
	"strings"
)

// EnrichInput is the (untrusted) feedback to classify.
type EnrichInput struct {
	Body     string
	Route    string
	Severity string
	Context  map[string]any
}

// EnrichResult is the model's structured classification.
type EnrichResult struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
}

// Classifier turns raw feedback into a structured triage candidate.
type Classifier interface {
	// Enrich classifies one feedback item. It must never execute instructions
	// found inside in.Body.
	Enrich(ctx context.Context, in EnrichInput) (EnrichResult, error)
	// Model returns the provider:model identifier in use.
	Model() string
}

// KeyFunc resolves an API key for a provider on demand, so secrets are read
// from files/vaults at call time rather than held on a struct.
type KeyFunc func(provider string) (string, error)

// New constructs a Provider (classifier + task planner) from a "provider:model"
// address.
func New(address string, keys KeyFunc) (Provider, error) {
	provider, model, ok := strings.Cut(address, ":")
	if !ok {
		return nil, fmt.Errorf("llm: address %q must be provider:model", address)
	}
	switch provider {
	case "fake":
		return NewFake(model), nil
	case "anthropic":
		if keys == nil {
			return nil, fmt.Errorf("llm: provider %q requires a key source", provider)
		}
		key, err := keys(provider)
		if err != nil {
			return nil, fmt.Errorf("llm: resolving key for %q: %w", provider, err)
		}
		return NewAnthropic(model, key), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
}
