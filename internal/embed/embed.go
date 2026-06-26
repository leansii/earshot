// SPDX-License-Identifier: Apache-2.0

// Package embed is a provider-agnostic embedding interface used for
// near-duplicate detection. Like llm, models are addressed as "provider:model".
//
// Earshot ships a deterministic "fake" embedder for offline tests/dev. A real
// provider (e.g. Voyage, OpenAI, a local model) implements Embedder and plugs
// in without touching the dedup logic.
package embed

import (
	"context"
	"fmt"
	"strings"
)

// Embedder turns text into a vector. Implementations should return vectors of a
// fixed dimension for a given model.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
	// Dim is the vector dimension this embedder produces.
	Dim() int
}

// New constructs an Embedder from a "provider:model" address. Only the fake
// provider is wired in this milestone; real providers are a follow-up.
func New(address string) (Embedder, error) {
	provider, model, ok := strings.Cut(address, ":")
	if !ok {
		return nil, fmt.Errorf("embed: address %q must be provider:model", address)
	}
	switch provider {
	case "fake":
		return NewFake(model), nil
	default:
		return nil, fmt.Errorf("embed: unknown provider %q (real providers are a follow-up)", provider)
	}
}
