/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package askuser

import (
	"context"
	"encoding/json"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const (
	// ToolName is the registered name for the ask_user tool.
	ToolName        = "ask_user"
	toolDescription = "Ask the user a clarifying question when the task is ambiguous " +
		"or critical information is missing. The user's free-form text response is " +
		"returned as the result. Use this sparingly -- only when the answer cannot " +
		"be reasonably inferred from context."

	defaultTimeout = 5 * time.Minute
)

// UserInteractor collects a free-form text response from a user.
// Implementations control how the question is presented and the response gathered
// (TUI dialog, HTTP callback, non-interactive fallback, etc.).
type UserInteractor interface {
	// AskUser presents the question to the user and returns their text response.
	// The context carries the configured timeout. If the context is canceled or
	// the timeout elapses, the implementation should return a timeout/fallback message
	// (not an error) so the agent can proceed.
	AskUser(ctx context.Context, question string) (string, error)
}

// AskUserTool holds configuration for the ask_user tool.
type AskUserTool struct {
	interactor UserInteractor
	timeout    time.Duration
}

// Option is a functional option for configuring an AskUserTool.
type Option func(*AskUserTool)

// WithTimeout sets the timeout for user responses.
func WithTimeout(d time.Duration) Option {
	return func(t *AskUserTool) { t.timeout = d }
}

// New creates an AskUserTool with the given interactor and options.
func New(interactor UserInteractor, opts ...Option) *AskUserTool {
	t := &AskUserTool{
		interactor: interactor,
		timeout:    defaultTimeout,
	}
	for _, o := range opts {
		o(t)
	}

	return t
}

// ToolDef returns the schema.ToolDef for registration.
func (t *AskUserTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ToolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The clarifying question to ask the user",
				},
			},
			"required":             []string{"question"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this ask_user tool.
func (t *AskUserTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed struct {
			Question string `json:"question"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "ask_user: invalid arguments: "+err.Error()), nil
		}

		if parsed.Question == "" {
			return schema.ErrorResult("", "ask_user: question must not be empty"), nil
		}

		// Apply timeout.
		askCtx, cancel := context.WithTimeout(ctx, t.timeout)
		defer cancel()

		response, err := t.interactor.AskUser(askCtx, parsed.Question)
		if err != nil {
			return schema.ErrorResult("", "ask_user: "+err.Error()), nil
		}

		return schema.TextResult("", response), nil
	}
}

// Register creates an AskUserTool and registers it in the given registry.
// Returns an error if a tool named "ask_user" is already registered.
func Register(registry *tool.Registry, interactor UserInteractor, opts ...Option) error {
	t := New(interactor, opts...)
	return registry.RegisterIfAbsent(t.ToolDef(), t.Handler())
}
