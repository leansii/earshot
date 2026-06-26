// SPDX-License-Identifier: Apache-2.0

// Package dedup finds near-duplicate feedback by comparing embedding vectors.
// A near-duplicate is linked to the existing item rather than creating a new
// one, so a flood of "the app crashed" reports collapses into a single issue.
package dedup

import "github.com/leansii/earshot/internal/core"

// DefaultThreshold is the cosine similarity at/above which two items are
// considered the same. Tunable per deployment.
const DefaultThreshold float32 = 0.92

// Cosine returns the cosine similarity of a and b. Vectors of differing length
// (e.g. produced by different embedding models) are treated as incomparable and
// return 0.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

// Match is the result of a nearest-neighbour search.
type Match struct {
	Item  core.Item
	Score float32
	// IsDup reports whether Score met the threshold.
	IsDup bool
}

// Nearest returns the most similar existing item to target. IsDup is true when
// the best score is >= threshold. Items without an embedding are skipped.
func Nearest(target []float32, items []core.Item, threshold float32) Match {
	var best Match
	for _, it := range items {
		if len(it.Embedding) == 0 {
			continue
		}
		s := Cosine(target, it.Embedding)
		if s > best.Score {
			best = Match{Item: it, Score: s}
		}
	}
	best.IsDup = best.Score >= threshold && best.Item.ID != ""
	return best
}

// sqrt is a tiny Newton's-method square root to avoid importing math for a
// single call in a hot loop helper. It is accurate enough for similarity.
func sqrt(x float32) float32 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 12; i++ {
		z = (z + x/z) / 2
	}
	return z
}
