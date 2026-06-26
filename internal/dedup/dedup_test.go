// SPDX-License-Identifier: Apache-2.0

package dedup

import (
	"testing"

	"github.com/leansii/earshot/internal/core"
)

func TestCosine(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Fatalf("identical vectors should be ~1, got %v", got)
	}
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors should be 0, got %v", got)
	}
	if got := Cosine([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("mismatched lengths should be 0, got %v", got)
	}
}

func TestNearest_DetectsDuplicate(t *testing.T) {
	items := []core.Item{
		{ID: "item_1", Embedding: []float32{1, 0, 0}},
		{ID: "item_2", Embedding: []float32{0, 1, 0}},
	}
	m := Nearest([]float32{0.99, 0.01, 0}, items, DefaultThreshold)
	if !m.IsDup {
		t.Fatalf("expected duplicate, got score %v", m.Score)
	}
	if m.Item.ID != "item_1" {
		t.Fatalf("expected item_1, got %s", m.Item.ID)
	}
}

func TestNearest_NoDuplicateBelowThreshold(t *testing.T) {
	items := []core.Item{{ID: "item_1", Embedding: []float32{1, 0, 0}}}
	m := Nearest([]float32{0, 1, 0}, items, DefaultThreshold)
	if m.IsDup {
		t.Fatalf("orthogonal vector should not be a duplicate (score %v)", m.Score)
	}
}

func TestNearest_EmptyCandidates(t *testing.T) {
	m := Nearest([]float32{1, 0}, nil, DefaultThreshold)
	if m.IsDup {
		t.Fatal("no candidates must not yield a duplicate")
	}
}
