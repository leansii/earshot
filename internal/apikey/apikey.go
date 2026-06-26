// SPDX-License-Identifier: Apache-2.0

// Package apikey generates and hashes per-app API keys. Raw keys are shown to
// the operator exactly once; only their SHA-256 hash is ever stored.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Prefix is prepended to every key so they are recognizable and greppable.
const Prefix = "esk_" // "earshot secret key"

// Generate returns a new random API key and its short, non-secret prefix
// (suitable for display in logs/dashboards).
func Generate() (key, displayPrefix string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("apikey: read random: %w", err)
	}
	key = Prefix + hex.EncodeToString(buf)
	return key, key[:len(Prefix)+8], nil
}

// Hash returns the hex SHA-256 of a raw key. Lookups compare hashes so the raw
// key never has to be stored.
func Hash(rawKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawKey)))
	return hex.EncodeToString(sum[:])
}

// DisplayPrefix returns the non-secret prefix portion of a raw key.
func DisplayPrefix(rawKey string) string {
	if len(rawKey) < len(Prefix)+8 {
		return rawKey
	}
	return rawKey[:len(Prefix)+8]
}
