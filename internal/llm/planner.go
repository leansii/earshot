// SPDX-License-Identifier: Apache-2.0

package llm

// This file adds the triage-agent capability: turning an enriched, SCRUBBED
// feedback-issue into a small set of actionable tasks. Like classification, it
// treats the feedback as data (fixed instructions, fenced input, no tools, no
// secrets), so injected "instructions" carry no authority.

import "context"

// Task is one actionable item proposed by the agent. It is a proposal only —
// nothing is pushed to a tracker until a human approves it.
type Task struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// PlanInput is the enriched feedback-issue handed to the agent. Body is already
// scrubbed; the agent never sees raw feedback.
type PlanInput struct {
	Category     string
	Title        string
	Summary      string
	ScrubbedBody string
}

// Planner decomposes a feedback-issue into tasks.
type Planner interface {
	DecomposeTasks(ctx context.Context, in PlanInput) ([]Task, error)
}

// Provider is a model that can both classify and plan — what llm.New returns.
type Provider interface {
	Classifier
	Planner
}
