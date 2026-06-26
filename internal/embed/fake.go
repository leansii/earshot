// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Fake is a deterministic, offline embedder. It builds a bag-of-words vector by
// hashing tokens into a fixed-dimension space, then L2-normalizes. Identical
// text yields identical vectors and similar text yields high cosine similarity,
// which is all the dedup logic needs to be exercised hermetically.
type Fake struct {
	model string
	dim   int
}

// NewFake returns a deterministic embedder. The model name may encode a
// dimension hint like "hash-256"; otherwise 256 is used.
func NewFake(model string) *Fake {
	dim := 256
	if _, after, ok := strings.Cut(model, "-"); ok {
		if d := atoiSafe(after); d > 0 {
			dim = d
		}
	}
	if model == "" {
		model = "hash-256"
	}
	return &Fake{model: model, dim: dim}
}

// Model implements Embedder.
func (f *Fake) Model() string { return "fake:" + f.model }

// Dim implements Embedder.
func (f *Fake) Dim() int { return f.dim }

// Embed implements Embedder.
func (f *Fake) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, f.dim)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		vec[h.Sum32()%uint32(f.dim)]++
	}
	// L2 normalize so cosine similarity is just a dot product.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		inv := float32(1.0 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec, nil
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
