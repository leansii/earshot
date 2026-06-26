// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"testing"
)

func TestFake_DeterministicAndSized(t *testing.T) {
	f := NewFake("hash-64")
	if f.Dim() != 64 {
		t.Fatalf("dim = %d, want 64", f.Dim())
	}
	ctx := context.Background()
	a, _ := f.Embed(ctx, "hello world")
	b, _ := f.Embed(ctx, "hello world")
	if len(a) != 64 {
		t.Fatalf("vector len = %d, want 64", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("same text must produce identical vectors")
		}
	}
}

func TestFake_SimilarTextHigherSimilarity(t *testing.T) {
	f := NewFake("hash-128")
	ctx := context.Background()
	base, _ := f.Embed(ctx, "the save button crashes the app")
	same, _ := f.Embed(ctx, "the save button crashes the app")
	diff, _ := f.Embed(ctx, "please add dark mode to settings")

	if dot(base, same) <= dot(base, diff) {
		t.Fatal("identical text should be at least as similar as unrelated text")
	}
}

func TestNew_UnknownProvider(t *testing.T) {
	if _, err := New("fake:hash-256"); err != nil {
		t.Fatalf("fake should work: %v", err)
	}
	if _, err := New("voyage:v3"); err == nil {
		t.Fatal("unimplemented provider should error")
	}
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
