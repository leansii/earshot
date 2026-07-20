// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat is a Classifier+Planner over an OpenAI-compatible
// /chat/completions endpoint. It backs the "deepseek" and "gemini" providers,
// which both expose that wire format, and — like the Anthropic client — is
// implemented against the raw HTTP API so the build stays reproducible.
//
// The same injection defense applies: fixed system prompts, the untrusted
// feedback fenced as data, no tools exposed, no secrets in the context.
type OpenAICompat struct {
	provider string
	model    string
	apiKey   string
	http     *http.Client
	url      string
}

// openAIEndpoints maps a provider name to its OpenAI-compatible completions URL.
var openAIEndpoints = map[string]string{
	"deepseek": "https://api.deepseek.com/chat/completions",
	"gemini":   "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
}

// NewOpenAICompat builds a classifier+planner for one of the OpenAI-compatible
// providers ("deepseek", "gemini").
func NewOpenAICompat(provider, model, apiKey string) (*OpenAICompat, error) {
	url, ok := openAIEndpoints[provider]
	if !ok {
		return nil, fmt.Errorf("llm: no OpenAI-compatible endpoint for provider %q", provider)
	}
	return &OpenAICompat{
		provider: provider,
		model:    model,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 30 * time.Second},
		url:      url,
	}, nil
}

// Model implements Classifier.
func (o *OpenAICompat) Model() string { return o.provider + ":" + o.model }

type openAIReq struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// complete performs one chat completion and returns the assistant text.
func (o *OpenAICompat) complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	reqBody, err := json.Marshal(openAIReq{
		Model:     o.model,
		MaxTokens: maxTokens,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+o.apiKey)

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%s: request failed: %w", o.provider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out openAIResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", o.provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := "unknown error"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return "", fmt.Errorf("%s: status %d: %s", o.provider, resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: empty response", o.provider)
	}
	return out.Choices[0].Message.Content, nil
}

// jsonText trims whitespace and the markdown code fence that smaller models
// sometimes wrap around JSON output despite instructions not to.
func jsonText(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Enrich implements Classifier.
func (o *OpenAICompat) Enrich(ctx context.Context, in EnrichInput) (EnrichResult, error) {
	// Fence the untrusted body as data.
	user := fmt.Sprintf("<feedback route=%q severity=%q>\n%s\n</feedback>",
		in.Route, in.Severity, in.Body)

	text, err := o.complete(ctx, systemPrompt, user, 512)
	if err != nil {
		return EnrichResult{}, err
	}
	var result EnrichResult
	if err := json.Unmarshal([]byte(jsonText(text)), &result); err != nil {
		return EnrichResult{}, fmt.Errorf("%s: model did not return JSON: %w", o.provider, err)
	}
	return result, nil
}

// DecomposeTasks implements Planner.
func (o *OpenAICompat) DecomposeTasks(ctx context.Context, in PlanInput) ([]Task, error) {
	user := fmt.Sprintf("<issue category=%q title=%q>\n%s\n\n%s\n</issue>",
		in.Category, in.Title, in.Summary, in.ScrubbedBody)

	text, err := o.complete(ctx, plannerSystemPrompt, user, 1024)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal([]byte(jsonText(text)), &tasks); err != nil {
		return nil, fmt.Errorf("%s: model did not return a JSON task array: %w", o.provider, err)
	}
	return tasks, nil
}
