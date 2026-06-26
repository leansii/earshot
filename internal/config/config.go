// SPDX-License-Identifier: Apache-2.0

// Package config loads runtime configuration from the environment. Secrets are
// read via *_FILE indirection wherever possible so tokens live in files/secret
// mounts rather than the environment of a long-lived process.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/leansii/earshot/internal/llm"
)

// Config is the process configuration.
type Config struct {
	HTTPAddr    string
	DatabaseURL string
	// Queue selects the backend: "memory" (dev/tests) or "river" (Postgres).
	Queue      string
	LLMModel   string
	EmbedModel string

	// anthropicKeyFile is the path to the Anthropic API key, if using that
	// provider. The key itself is read lazily via KeyFunc.
	anthropicKeyFile string
}

// Load reads configuration from the environment, applying safe defaults.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:         env("EARSHOT_HTTP_ADDR", ":8080"),
		DatabaseURL:      env("EARSHOT_DATABASE_URL", ""),
		Queue:            env("EARSHOT_QUEUE", "memory"),
		LLMModel:         env("EARSHOT_LLM_MODEL", "fake:classifier-v1"),
		EmbedModel:       env("EARSHOT_EMBED_MODEL", "fake:hash-256"),
		anthropicKeyFile: env("EARSHOT_ANTHROPIC_API_KEY_FILE", ""),
	}
	switch c.Queue {
	case "memory", "river":
	default:
		return Config{}, fmt.Errorf("config: EARSHOT_QUEUE must be memory|river, got %q", c.Queue)
	}
	if c.Queue == "river" && c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: EARSHOT_QUEUE=river requires EARSHOT_DATABASE_URL")
	}
	return c, nil
}

// KeyFunc returns an llm.KeyFunc that reads provider keys from files at call
// time. Only providers that need keys are handled; "fake" needs none.
func (c Config) KeyFunc() llm.KeyFunc {
	return func(provider string) (string, error) {
		switch provider {
		case "anthropic":
			if c.anthropicKeyFile == "" {
				// Fall back to a direct env var if no file is configured.
				if k := os.Getenv("EARSHOT_ANTHROPIC_API_KEY"); k != "" {
					return k, nil
				}
				return "", fmt.Errorf("config: set EARSHOT_ANTHROPIC_API_KEY_FILE")
			}
			return readSecretFile(c.anthropicKeyFile)
		default:
			return "", fmt.Errorf("config: no key source for provider %q", provider)
		}
	}
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("config: read secret file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
