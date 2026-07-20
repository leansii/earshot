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

	// keyFiles maps an LLM provider name to the path of its API key file, if
	// configured. Keys themselves are read lazily via KeyFunc.
	keyFiles map[string]string
}

// llmKeyEnv maps each keyed LLM provider to its environment variable names:
// EARSHOT_<X>_API_KEY_FILE (preferred, a path) and EARSHOT_<X>_API_KEY
// (direct fallback).
var llmKeyEnv = map[string]string{
	"anthropic": "EARSHOT_ANTHROPIC_API_KEY",
	"deepseek":  "EARSHOT_DEEPSEEK_API_KEY",
	"gemini":    "EARSHOT_GEMINI_API_KEY",
}

// Load reads configuration from the environment, applying safe defaults.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:    env("EARSHOT_HTTP_ADDR", ":8080"),
		DatabaseURL: env("EARSHOT_DATABASE_URL", ""),
		Queue:       env("EARSHOT_QUEUE", "memory"),
		LLMModel:    env("EARSHOT_LLM_MODEL", "fake:classifier-v1"),
		EmbedModel:  env("EARSHOT_EMBED_MODEL", "fake:hash-256"),
		keyFiles:    map[string]string{},
	}
	for provider, envVar := range llmKeyEnv {
		if f := env(envVar+"_FILE", ""); f != "" {
			c.keyFiles[provider] = f
		}
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
		envVar, ok := llmKeyEnv[provider]
		if !ok {
			return "", fmt.Errorf("config: no key source for provider %q", provider)
		}
		if f := c.keyFiles[provider]; f != "" {
			return readSecretFile(f)
		}
		// Fall back to a direct env var if no file is configured.
		if k := os.Getenv(envVar); k != "" {
			return k, nil
		}
		return "", fmt.Errorf("config: set %s_FILE", envVar)
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
