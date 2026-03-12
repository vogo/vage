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

package server

import (
	"context"
	"testing"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

func TestNewServer(t *testing.T) {
	s := NewServer()
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	if s.server == nil {
		t.Error("expected non-nil internal server")
	}
}

func TestServer_InterfaceCompliance(t *testing.T) {
	var _ MCPServer = (*Server)(nil)
}

func TestServer_ServerAccessor(t *testing.T) {
	s := NewServer()
	if s.Server() == nil {
		t.Error("expected non-nil Server() return")
	}

	if s.Server() != s.server {
		t.Error("expected Server() to return internal server")
	}
}

type testAgent struct {
	agent.Base
}

func (a *testAgent) Run(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	text := ""
	if len(req.Messages) > 0 {
		text = "echo: " + req.Messages[0].Content.Text()
	}

	return &schema.RunResponse{
		Messages: []schema.Message{schema.NewAssistantMessage(
			schema.NewUserMessage(text).Message,
			a.ID(),
		)},
	}, nil
}

func TestServer_RegisterAgent(t *testing.T) {
	s := NewServer()

	a := &testAgent{
		Base: agent.NewBase(agent.Config{
			ID:          "test-agent",
			Name:        "Test Agent",
			Description: "A test agent",
		}),
	}

	err := s.RegisterAgent(a)
	if err != nil {
		t.Fatalf("unexpected error registering agent: %v", err)
	}
}

func TestServer_RegisterTool(t *testing.T) {
	s := NewServer()

	err := s.RegisterTool(ToolRegistration{
		Name:        "test-tool",
		Description: "A test tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type": "string",
				},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
			return schema.TextResult("", "result"), nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error registering tool: %v", err)
	}
}

func TestServer_RegisterMultipleTools(t *testing.T) {
	s := NewServer()

	inputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"input": map[string]any{"type": "string"}},
	}

	for i, name := range []string{"tool-a", "tool-b", "tool-c"} {
		err := s.RegisterTool(ToolRegistration{
			Name:        name,
			Description: "Tool " + string(rune('A'+i)),
			InputSchema: inputSchema,
			Handler: func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
				return schema.TextResult("", "ok"), nil
			},
		})
		if err != nil {
			t.Fatalf("unexpected error registering %s: %v", name, err)
		}
	}
}

func TestServer_RegisterAgentAndTool(t *testing.T) {
	s := NewServer()

	a := &testAgent{
		Base: agent.NewBase(agent.Config{
			ID:          "agent-1",
			Name:        "Agent 1",
			Description: "First agent",
		}),
	}

	if err := s.RegisterAgent(a); err != nil {
		t.Fatalf("unexpected error registering agent: %v", err)
	}

	err := s.RegisterTool(ToolRegistration{
		Name:        "custom-tool",
		Description: "Custom tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"input": map[string]any{"type": "string"}},
		},
		Handler: func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
			return schema.TextResult("", "result"), nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error registering tool: %v", err)
	}
}
