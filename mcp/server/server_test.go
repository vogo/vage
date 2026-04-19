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

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/security/credscrub"
)

func TestNewServer(t *testing.T) {
	s := NewServer()
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	if s.server == nil {
		t.Error("expected non-nil internal server")
	}

	if s.scanner != nil {
		t.Error("expected nil scanner when no option provided")
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
			_ = args

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

func TestServer_WithCredentialScanner(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})
	srv := NewServer(WithCredentialScanner(s))

	if srv.scanner != s {
		t.Error("expected scanner to be installed by option")
	}
}

func TestServer_WithScanCallback(t *testing.T) {
	called := false
	cb := func(_ context.Context, _ ScanEvent) { called = true }
	srv := NewServer(WithScanCallback(cb))

	srv.onScan(t.Context(), ScanEvent{Direction: DirectionServerInbound})
	if !called {
		t.Error("stored callback did not fire")
	}
}

func TestServer_ApplyInboundScan_Redact(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})
	srv := NewServer(WithCredentialScanner(s))

	args, blockResp := srv.applyInboundScan(t.Context(), "tool", []byte(`{"password":"hunter2"}`))
	if blockResp != nil {
		t.Fatalf("redact should not block; got %+v", blockResp)
	}

	if args["password"] != "[REDACTED:password]" {
		t.Errorf("expected password redaction, got %v", args["password"])
	}
}

func TestServer_ApplyInboundScan_Block(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})
	srv := NewServer(WithCredentialScanner(s))

	_, blockResp := srv.applyInboundScan(t.Context(), "tool", []byte(`{"password":"hunter2"}`))
	if blockResp == nil {
		t.Fatal("block action should produce a block response")
	}

	if !blockResp.IsError {
		t.Error("block response should have IsError=true")
	}
}

func TestServer_ApplyInboundScan_NoScanner(t *testing.T) {
	srv := NewServer()

	args, blockResp := srv.applyInboundScan(t.Context(), "tool", []byte(`{"password":"hunter2"}`))
	if blockResp != nil {
		t.Error("no scanner should not block")
	}

	if args["password"] != "hunter2" {
		t.Error("no scanner should not mutate")
	}
}

func TestServer_ApplyOutboundTextScan_Redact(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})
	srv := NewServer(WithCredentialScanner(s))

	out, blockResp := srv.applyOutboundTextScan(t.Context(), "tool", "key=AKIAIOSFODNN7EXAMPLE")
	if blockResp != nil {
		t.Fatal("redact should not block")
	}

	if out == "key=AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("expected redaction; got %q", out)
	}
}

func TestServer_ApplyOutboundTextScan_Block(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})
	srv := NewServer(WithCredentialScanner(s))

	_, blockResp := srv.applyOutboundTextScan(t.Context(), "tool", "key=AKIAIOSFODNN7EXAMPLE")
	if blockResp == nil {
		t.Fatal("block action should produce a block response")
	}

	if !blockResp.IsError {
		t.Error("block response should have IsError=true")
	}
}

func TestServer_CallbackFiresOnHit(t *testing.T) {
	events := []ScanEvent{}
	cb := func(_ context.Context, ev ScanEvent) { events = append(events, ev) }

	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})
	srv := NewServer(WithCredentialScanner(s), WithScanCallback(cb))

	srv.applyInboundScan(t.Context(), "tool", []byte(`{"password":"hunter2"}`))

	if len(events) != 1 {
		t.Fatalf("expected 1 callback invocation; got %d", len(events))
	}

	if events[0].Direction != DirectionServerInbound {
		t.Errorf("want inbound direction; got %q", events[0].Direction)
	}

	if events[0].ToolName != "tool" {
		t.Errorf("want tool name; got %q", events[0].ToolName)
	}
}
