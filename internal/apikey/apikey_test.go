// SPDX-License-Identifier: Apache-2.0

package apikey

import (
	"strings"
	"testing"
)

func TestGenerateAndHash(t *testing.T) {
	key, prefix, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Fatalf("key %q missing prefix %q", key, Prefix)
	}
	if !strings.HasPrefix(key, prefix) {
		t.Fatalf("display prefix %q is not a prefix of %q", prefix, key)
	}
	if DisplayPrefix(key) != prefix {
		t.Fatalf("DisplayPrefix mismatch: %q vs %q", DisplayPrefix(key), prefix)
	}

	h := Hash(key)
	if len(h) != 64 {
		t.Fatalf("hash should be 64 hex chars, got %d", len(h))
	}
	if Hash(key) != h {
		t.Fatal("hash must be deterministic")
	}
	if Hash(key+"x") == h {
		t.Fatal("different keys must hash differently")
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k, _, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if seen[k] {
			t.Fatal("duplicate key generated")
		}
		seen[k] = true
	}
}
