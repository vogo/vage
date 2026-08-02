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

package mcp_tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vogo/vage/agent"
	mcpclient "github.com/vogo/vage/mcp/client"
	mcpserver "github.com/vogo/vage/mcp/server"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// TestClientCallsServerTools tests the scenario where an MCP client discovers
// and calls tools registered on an MCP server.
func TestClientCallsServerTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// Create server with a calculator "add" tool.
	srv := mcpserver.NewServer()

	err := srv.RegisterTool(mcpserver.ToolRegistration{
		Name:        "add",
		Description: "Add two numbers",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "number"},
				"b": map[string]any{"type": "number"},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
			a, _ := args["a"].(float64)
			b, _ := args["b"].(float64)
			return schema.TextResult("", fmt.Sprintf("%.0f", a+b)), nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	// Start server in background.
	go func() { _ = srv.Serve(ctx, serverTransport) }()

	// Create client and connect.
	cli := mcpclient.NewClient("test://server")

	if err := cli.Connect(ctx, clientTransport); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Discover tools.
	tools, err := cli.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}

	if tools[0].Name != "add" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "add")
	}

	if tools[0].Source != schema.ToolSourceMCP {
		t.Errorf("tool source = %q, want %q", tools[0].Source, schema.ToolSourceMCP)
	}

	// Call the add tool.
	result, err := cli.CallTool(ctx, "add", `{"a": 3, "b": 4}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if result.IsError {
		t.Fatalf("tool result is error")
	}

	if len(result.Content) == 0 {
		t.Fatalf("no content in result")
	}

	if result.Content[0].Text != "7" {
		t.Errorf("result text = %q, want %q", result.Content[0].Text, "7")
	}

	// Ping.
	if err := cli.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}

	// Disconnect.
	if err := cli.Disconnect(); err != nil {
		t.Errorf("Disconnect: %v", err)
	}

	cancel()
}

// echoAgent is a simple agent for testing that echoes back the input.
type echoAgent struct {
	agent.Base
}

func newEchoAgent(id, name, desc string) *echoAgent {
	return &echoAgent{
		Base: agent.NewBase(agent.Config{ID: id, Name: name, Description: desc}),
	}
}

func (e *echoAgent) Run(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	input := ""
	if len(req.Messages) > 0 {
		input = req.Messages[0].Text()
	}
	return &schema.RunResponse{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "echo: "+input)},
	}, nil
}

// TestAgentExposedAsMCPTool tests the scenario where a vage Agent is
// registered on an MCP server and invoked by an MCP client.
func TestAgentExposedAsMCPTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// Create server and register the echo agent.
	srv := mcpserver.NewServer()

	echoAg := newEchoAgent("echo-agent", "Echo Agent", "Echoes back input text")
	if err := srv.RegisterAgent(echoAg); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Start server.
	go func() { _ = srv.Serve(ctx, serverTransport) }()

	// Create client.
	cli := mcpclient.NewClient("test://server")

	if err := cli.Connect(ctx, clientTransport); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// List tools - should see the agent.
	tools, err := cli.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}

	if tools[0].Name != "echo-agent" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "echo-agent")
	}

	// Call the agent tool.
	result, err := cli.CallTool(ctx, "echo-agent", `{"input": "hello world"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if result.IsError {
		t.Fatalf("tool result is error")
	}

	if len(result.Content) == 0 {
		t.Fatalf("no content in result")
	}

	if !strings.Contains(result.Content[0].Text, "echo: hello world") {
		t.Errorf("result text = %q, want to contain %q", result.Content[0].Text, "echo: hello world")
	}

	if err := cli.Disconnect(); err != nil {
		t.Errorf("Disconnect: %v", err)
	}

	cancel()
}

// TestEndToEndBidirectional tests Agent B calling Agent A via MCP.
// Agent A is exposed as an MCP server tool.
// Agent B uses an MCP client to call Agent A through the tool registry.
func TestEndToEndBidirectional(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// Agent A: an echo agent exposed via MCP server.
	agentA := newEchoAgent("agent-a", "Agent A", "Agent A echoes input")
	srv := mcpserver.NewServer()
	if err := srv.RegisterAgent(agentA); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	go func() { _ = srv.Serve(ctx, serverTransport) }()

	// Create MCP client for Agent B.
	cli := mcpclient.NewClient("test://agent-a")

	if err := cli.Connect(ctx, clientTransport); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Discover tools and merge into Agent B's registry.
	tools, err := cli.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	registry := tool.NewRegistry(tool.WithExternalCaller(cli))
	registry.Merge(tools)

	// Verify the tool is registered.
	def, ok := registry.Get("agent-a")
	if !ok {
		t.Fatal("agent-a tool not found in registry")
	}

	if def.Source != schema.ToolSourceMCP {
		t.Errorf("source = %q, want %q", def.Source, schema.ToolSourceMCP)
	}

	// Execute the MCP tool through the registry.
	argsJSON, _ := json.Marshal(map[string]any{"input": "bidirectional test"})
	result, err := registry.Execute(ctx, "agent-a", string(argsJSON))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.IsError {
		t.Fatalf("tool result is error")
	}

	if len(result.Content) == 0 {
		t.Fatalf("no content in result")
	}

	if !strings.Contains(result.Content[0].Text, "echo: bidirectional test") {
		t.Errorf("result text = %q, want to contain %q", result.Content[0].Text, "echo: bidirectional test")
	}

	if err := cli.Disconnect(); err != nil {
		t.Errorf("Disconnect: %v", err)
	}

	cancel()
}

// TestRegistryExternalCallerDelegation verifies that the enhanced Registry
// properly delegates to ExternalToolCaller for tools with no local handler.
func TestRegistryExternalCallerDelegation(t *testing.T) {
	ctx := context.Background()

	registry := tool.NewRegistry()

	// Register an MCP-sourced tool (no handler).
	registry.Merge([]schema.ToolDef{
		{Name: "remote-tool", Description: "A remote MCP tool", Source: schema.ToolSourceMCP},
	})

	// Without ExternalToolCaller, execution should fail.
	_, err := registry.Execute(ctx, "remote-tool", `{}`)
	if err == nil {
		t.Fatal("expected error without ExternalToolCaller")
	}

	// Set up a mock ExternalToolCaller.
	mock := &mockExternalCaller{
		result: schema.TextResult("", "mock result"),
	}
	registry.SetExternalCaller(mock)

	// Now execution should succeed.
	result, err := registry.Execute(ctx, "remote-tool", `{"key":"value"}`)
	if err != nil {
		t.Fatalf("Execute with ExternalToolCaller: %v", err)
	}

	if result.Content[0].Text != "mock result" {
		t.Errorf("result = %q, want %q", result.Content[0].Text, "mock result")
	}

	if mock.calledName != "remote-tool" {
		t.Errorf("called name = %q, want %q", mock.calledName, "remote-tool")
	}

	if mock.calledArgs != `{"key":"value"}` {
		t.Errorf("called args = %q, want %q", mock.calledArgs, `{"key":"value"}`)
	}
}

type mockExternalCaller struct {
	result     schema.ToolResult
	calledName string
	calledArgs string
}

func (m *mockExternalCaller) CallTool(_ context.Context, name, args string) (schema.ToolResult, error) {
	m.calledName = name
	m.calledArgs = args
	return m.result, nil
}
