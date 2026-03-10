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
	"errors"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

// mockAgent implements agent.Agent for testing.
type mockAgent struct {
	agent.Base
	runFunc func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
}

func (m *mockAgent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return m.runFunc(ctx, req)
}

func newMockAgent(id, name, desc string, runFunc func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)) *mockAgent {
	return &mockAgent{
		Base:    agent.NewBase(agent.Config{ID: id, Name: name, Description: desc}),
		runFunc: runFunc,
	}
}

func TestRegister(t *testing.T) {
	t.Run("DefaultExecution", func(t *testing.T) {
		ag := newMockAgent("agent-1", "test-agent", "A test agent", func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			input := req.Messages[0].Content.Text()
			return &schema.RunResponse{
				Messages: []schema.Message{
					{
						Message: aimodel.Message{
							Role:    aimodel.RoleAssistant,
							Content: aimodel.NewTextContent("Echo: " + input),
						},
					},
				},
			}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		result, err := r.Execute(context.Background(), "test-agent", `{"input":"hello"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		if result.Content[0].Text != "Echo: hello" {
			t.Errorf("result text = %q, want %q", result.Content[0].Text, "Echo: hello")
		}
	})

	t.Run("CustomOverrides", func(t *testing.T) {
		ag := newMockAgent("agent-2", "original-name", "original desc", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		regErr := Register(r, ag,
			WithName("custom-name"),
			WithDescription("custom description"),
		)
		if regErr != nil {
			t.Fatalf("Register error: %v", regErr)
		}

		def, ok := r.Get("custom-name")
		if !ok {
			t.Fatal("Get returned false for custom-name")
		}

		if def.Name != "custom-name" {
			t.Errorf("Name = %q, want %q", def.Name, "custom-name")
		}

		if def.Description != "custom description" {
			t.Errorf("Description = %q, want %q", def.Description, "custom description")
		}

		_, ok = r.Get("original-name")
		if ok {
			t.Error("Get returned true for original-name, expected false")
		}
	})

	t.Run("ListSource", func(t *testing.T) {
		ag := newMockAgent("agent-3", "source-agent", "test source", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		defs := r.List()
		if len(defs) != 1 {
			t.Fatalf("len(List) = %d, want 1", len(defs))
		}

		if defs[0].Source != schema.ToolSourceAgent {
			t.Errorf("Source = %q, want %q", defs[0].Source, schema.ToolSourceAgent)
		}

		if defs[0].AgentID != "agent-3" {
			t.Errorf("AgentID = %q, want %q", defs[0].AgentID, "agent-3")
		}
	})

	t.Run("MalformedJSON", func(t *testing.T) {
		ag := newMockAgent("agent-4", "json-agent", "test", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		result, err := r.Execute(context.Background(), "json-agent", "not json")
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for malformed JSON")
		}

		if !strings.HasPrefix(result.Content[0].Text, "agent tool: invalid arguments:") {
			t.Errorf("error text = %q, want prefix %q", result.Content[0].Text, "agent tool: invalid arguments:")
		}
	})

	t.Run("MissingInput", func(t *testing.T) {
		ag := newMockAgent("agent-5", "missing-input-agent", "test", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		result, err := r.Execute(context.Background(), "missing-input-agent", `{"other":"value"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for missing input")
		}

		if result.Content[0].Text != "agent tool: 'input' field must be a non-empty string" {
			t.Errorf("error text = %q, want %q", result.Content[0].Text, "agent tool: 'input' field must be a non-empty string")
		}
	})

	t.Run("AgentError", func(t *testing.T) {
		ag := newMockAgent("agent-6", "error-agent", "test", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, errors.New("something went wrong")
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		result, err := r.Execute(context.Background(), "error-agent", `{"input":"test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for agent error")
		}

		if !strings.HasPrefix(result.Content[0].Text, "agent tool: execution failed:") {
			t.Errorf("error text = %q, want prefix %q", result.Content[0].Text, "agent tool: execution failed:")
		}
	})

	t.Run("EmptyResponse", func(t *testing.T) {
		ag := newMockAgent("agent-7", "empty-agent", "test", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		result, err := r.Execute(context.Background(), "empty-agent", `{"input":"test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		if result.Content[0].Text != "" {
			t.Errorf("result text = %q, want empty string", result.Content[0].Text)
		}
	})

	t.Run("CustomParameters", func(t *testing.T) {
		ag := newMockAgent("agent-params", "params-agent", "test params", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		customParams := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Custom input description",
				},
				"extra": map[string]any{
					"type":        "string",
					"description": "An extra field",
				},
			},
			"required": []any{"input"},
		}

		r := tool.NewRegistry()
		err := Register(r, ag, WithParameters(customParams))
		if err != nil {
			t.Fatalf("Register error: %v", err)
		}

		def, ok := r.Get("params-agent")
		if !ok {
			t.Fatal("Get returned false for params-agent")
		}

		params, ok := def.Parameters.(map[string]any)
		if !ok {
			t.Fatal("Parameters is not map[string]any")
		}

		props, ok := params["properties"].(map[string]any)
		if !ok {
			t.Fatal("properties is not map[string]any")
		}

		if _, ok := props["extra"]; !ok {
			t.Error("custom parameters missing 'extra' property")
		}
	})

	t.Run("CustomArgExtractor", func(t *testing.T) {
		ag := newMockAgent("agent-extractor", "extractor-agent", "test extractor", func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			input := req.Messages[0].Content.Text()
			return &schema.RunResponse{
				Messages: []schema.Message{
					{
						Message: aimodel.Message{
							Role:    aimodel.RoleAssistant,
							Content: aimodel.NewTextContent("Got: " + input),
						},
					},
				},
			}, nil
		})

		extractor := func(parsed map[string]any) (string, error) {
			query, _ := parsed["query"].(string)
			lang, _ := parsed["lang"].(string)
			return query + " [" + lang + "]", nil
		}

		r := tool.NewRegistry()
		regErr := Register(r, ag,
			WithArgExtractor(extractor),
		)
		if regErr != nil {
			t.Fatalf("Register error: %v", regErr)
		}

		result, err := r.Execute(context.Background(), "extractor-agent", `{"query":"hello","lang":"fr"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		want := "Got: hello [fr]"
		if result.Content[0].Text != want {
			t.Errorf("result text = %q, want %q", result.Content[0].Text, want)
		}
	})

	t.Run("DuplicateName", func(t *testing.T) {
		ag1 := newMockAgent("agent-8a", "dup-agent", "first", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})
		ag2 := newMockAgent("agent-8b", "dup-agent", "second", func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

		r := tool.NewRegistry()
		if err := Register(r, ag1); err != nil {
			t.Fatalf("first Register error: %v", err)
		}

		dupErr := Register(r, ag2)
		if dupErr == nil {
			t.Fatal("expected error for duplicate agent name")
		}

		if !strings.Contains(dupErr.Error(), "already registered") {
			t.Errorf("error = %q, want to contain %q", dupErr.Error(), "already registered")
		}
	})
}
