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

package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// ArgExtractor extracts the input string from parsed tool arguments.
// The default extractor reads the "input" field; custom extractors can
// handle richer parameter schemas.
type ArgExtractor func(parsed map[string]any) (string, error)

// defaultArgExtractor extracts the "input" string field from parsed arguments.
func defaultArgExtractor(parsed map[string]any) (string, error) {
	inputVal, ok := parsed["input"]
	if !ok {
		return "", errMissingInput
	}

	input, ok := inputVal.(string)
	if !ok {
		return "", errMissingInput
	}

	return input, nil
}

// config holds configuration for registering an agent as a tool.
type config struct {
	name         string
	description  string
	parameters   any
	argExtractor ArgExtractor
	session      *sessionConfig // nil ⇒ no child-session wiring
}

// Option is a functional option for configuring agent-as-tool registration.
type Option func(*config)

// WithName overrides the tool name (defaults to agent.Name()).
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithDescription overrides the tool description (defaults to agent.Description()).
func WithDescription(desc string) Option {
	return func(c *config) { c.description = desc }
}

// WithParameters overrides the JSON Schema parameters.
// When using a custom schema, also provide WithArgExtractor to
// match the new schema, otherwise the default extractor (which reads "input") is used.
func WithParameters(params any) Option {
	return func(c *config) { c.parameters = params }
}

// WithArgExtractor overrides how raw JSON arguments are converted
// to the input string sent to the agent. This is useful when the parameter
// schema contains fields beyond the default "input" property.
func WithArgExtractor(fn ArgExtractor) Option {
	return func(c *config) { c.argExtractor = fn }
}

// defaultParams returns the default JSON Schema for agent tool parameters.
func defaultParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]any{
				"type":        "string",
				"description": "The input text to send to the agent",
			},
		},
		"required": []string{"input"},
	}
}

// Register registers an Agent as a callable tool in the registry.
func Register(registry *tool.Registry, ag agent.Agent, opts ...Option) error {
	cfg := config{
		name:         ag.Name(),
		description:  ag.Description(),
		parameters:   defaultParams(),
		argExtractor: defaultArgExtractor,
	}

	for _, o := range opts {
		o(&cfg)
	}

	def := schema.ToolDef{
		Name:        cfg.name,
		Description: cfg.description,
		Parameters:  cfg.parameters,
		Source:      schema.ToolSourceAgent,
		AgentID:     ag.ID(),
	}

	handler := newHandler(ag, cfg.argExtractor, cfg.session)

	return registry.RegisterIfAbsent(def, handler)
}

// agentToolError is a sentinel type for agent tool argument errors.
type agentToolError struct{ msg string }

func (e *agentToolError) Error() string { return e.msg }

var errMissingInput = &agentToolError{msg: "agent tool: 'input' field must be a non-empty string"}

// newHandler creates a ToolHandler closure that delegates to the given agent.
//
// Error policy: agent execution errors are returned as ToolResult with IsError=true
// rather than as Go errors. This keeps the error visible to the LLM in a tool-calling
// loop so it can retry or inform the user, instead of aborting the entire chain.
//
// When sessCfg is non-nil, the handler mints a child session per call and
// runs the subagent under that session's id (parent_id linked back to
// the parent's session id from ctx). See session.go for the wiring.
func newHandler(ag agent.Agent, extract ArgExtractor, sessCfg *sessionConfig) tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "agent tool: invalid arguments: "+err.Error()), nil
		}

		input, err := extract(parsed)
		if err != nil {
			return schema.ErrorResult("", err.Error()), nil
		}

		req := schema.RunRequest{
			Messages: []schema.Message{schema.NewUserMessage(input)},
		}

		runCtx, childSID, sessionErr := setupChildSession(ctx, sessCfg, ag, input)
		if sessionErr != nil {
			return schema.ErrorResult("", "agent tool: session setup failed: "+sessionErr.Error()), nil
		}

		resp, err := ag.Run(runCtx, &req)
		if err != nil {
			return schema.ErrorResult("", "agent tool: execution failed: "+err.Error()), nil
		}

		var parts []string
		for _, msg := range resp.Messages {
			if msg.Role == aimodel.RoleAssistant {
				text := msg.Content.Text()
				if text != "" {
					parts = append(parts, text)
				}
			}
		}

		body := strings.Join(parts, "\n")
		if childSID != "" {
			// Annotate so the parent LLM can navigate to the child's
			// records (e.g. via GET /v1/sessions/{id}/children).
			// Format kept terse — one trailing line, no Markdown.
			body = fmt.Sprintf("[child_session=%s]\n%s", childSID, body)
		}

		return schema.TextResult("", body), nil
	}
}
