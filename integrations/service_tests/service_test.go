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

package service_tests //nolint:revive // integration test package

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/service"
	"github.com/vogo/vagent/tool"
)

// newTestService creates a Service with mock agents and tools registered,
// and returns an httptest.Server for testing.
func newTestService(t *testing.T) (*service.Service, *httptest.Server) {
	t.Helper()

	// Register tools.
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{
		Name:        "test_tool",
		Description: "A test tool",
		Source:      schema.ToolSourceLocal,
	}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.TextResult("", "result"), nil
	})

	svc := service.New(service.Config{Addr: ":0"}, service.WithToolRegistry(reg))

	// Register a simple echo agent.
	echoAgent := agent.NewCustomAgent(agent.Config{
		ID:          "echo",
		Name:        "Echo Agent",
		Description: "Echoes back the user message",
	}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		var text string
		if len(req.Messages) > 0 {
			text = req.Messages[0].Content.Text()
		}

		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewAssistantMessage(
				aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("echo: " + text)},
				"echo",
			)},
			SessionID: req.SessionID,
		}, nil
	})

	svc.RegisterAgent(echoAgent)

	// Register a slow agent for async testing.
	slowAgent := agent.NewCustomAgent(agent.Config{
		ID:          "slow",
		Name:        "Slow Agent",
		Description: "Takes a moment to respond",
	}, func(ctx context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewAssistantMessage(
				aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("done")},
				"slow",
			)},
		}, nil
	})

	svc.RegisterAgent(slowAgent)

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)

	return svc, ts
}

func TestHealthEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/health")
	if err != nil {
		t.Fatalf("GET /v1/health failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify X-Request-ID is present.
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header")
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["status"] != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", body["status"])
	}
}

func TestListAgentsEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/agents")
	if err != nil {
		t.Fatalf("GET /v1/agents failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var agents []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	// Agents should be sorted by ID.
	if agents[0]["id"] != "echo" || agents[1]["id"] != "slow" {
		t.Fatalf("expected agents sorted as [echo, slow], got [%s, %s]", agents[0]["id"], agents[1]["id"])
	}
}

func TestGetAgentEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/agents/echo")
	if err != nil {
		t.Fatalf("GET /v1/agents/echo failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var agentInfo map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&agentInfo); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if agentInfo["id"] != "echo" {
		t.Fatalf("expected agent ID %q, got %q", "echo", agentInfo["id"])
	}

	if agentInfo["name"] != "Echo Agent" {
		t.Fatalf("expected agent name %q, got %q", "Echo Agent", agentInfo["name"])
	}
}

func TestGetAgentNotFoundEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/agents/nonexistent")
	if err != nil {
		t.Fatalf("GET /v1/agents/nonexistent failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}

	// Verify structured error response.
	var errResp map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp["code"] != "agent_not_found" {
		t.Fatalf("expected error code %q, got %q", "agent_not_found", errResp["code"])
	}
}

func TestRunEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/echo/run", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/echo/run failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var runResp schema.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(runResp.Messages) == 0 {
		t.Fatal("expected at least one message in response")
	}

	text := runResp.Messages[0].Content.Text()
	if !strings.Contains(text, "echo:") {
		t.Fatalf("expected echo response, got %q", text)
	}
}

func TestRunEndpointAgentNotFound(t *testing.T) {
	_, ts := newTestService(t)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/nonexistent/run", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/nonexistent/run failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestRunEndpointInvalidBody(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Post(ts.URL+"/v1/agents/echo/run", "application/json", strings.NewReader("invalid"))
	if err != nil {
		t.Fatalf("POST /v1/agents/echo/run failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.StatusCode)
	}
}

func TestStreamEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/echo/stream", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/echo/stream failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, body)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Fatalf("expected Content-Type %q, got %q", "text/event-stream", contentType)
	}

	// Read SSE events.
	var events []string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			events = append(events, after)
		}
	}

	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	// Should have agent_start and agent_end events (from RunToStream wrapper).
	hasStart := false
	hasEnd := false

	for _, e := range events {
		if e == "agent_start" {
			hasStart = true
		}

		if e == "agent_end" {
			hasEnd = true
		}
	}

	if !hasStart {
		t.Fatal("expected agent_start event")
	}

	if !hasEnd {
		t.Fatal("expected agent_end event")
	}
}

func TestStreamEndpointAgentNotFound(t *testing.T) {
	_, ts := newTestService(t)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/nonexistent/stream", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/nonexistent/stream failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestAsyncAndTaskEndpoints(t *testing.T) {
	_, ts := newTestService(t)

	// Start async task.
	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/slow/async", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/slow/async failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 202, got %d: %s", resp.StatusCode, body)
	}

	var asyncResp map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&asyncResp); err != nil {
		t.Fatalf("failed to decode async response: %v", err)
	}

	taskID := asyncResp["task_id"]
	if taskID == "" {
		t.Fatal("expected non-empty task_id")
	}

	// Poll for task completion.
	var task map[string]any

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		taskResp, err := http.Get(ts.URL + "/v1/tasks/" + taskID)
		if err != nil {
			t.Fatalf("GET /v1/tasks/%s failed: %v", taskID, err)
		}

		if err := json.NewDecoder(taskResp.Body).Decode(&task); err != nil {
			_ = taskResp.Body.Close()
			t.Fatalf("failed to decode task response: %v", err)
		}

		_ = taskResp.Body.Close()

		status, _ := task["status"].(string)
		if status == "completed" || status == "failed" {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	status, _ := task["status"].(string)
	if status != "completed" {
		t.Fatalf("expected task status %q, got %q", "completed", status)
	}

	// Verify the response is present.
	if task["response"] == nil {
		t.Fatal("expected task response to be present")
	}
}

func TestAsyncCancelEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	// Start a slow async task.
	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/slow/async", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/slow/async failed: %v", err)
	}

	var asyncResp map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&asyncResp)
	_ = resp.Body.Close()

	taskID := asyncResp["task_id"]

	// Cancel the task.
	cancelReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/tasks/"+taskID+"/cancel", nil)
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatalf("POST /v1/tasks/%s/cancel failed: %v", taskID, err)
	}
	defer func() { _ = cancelResp.Body.Close() }()

	if cancelResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(cancelResp.Body)
		t.Fatalf("expected status 200, got %d: %s", cancelResp.StatusCode, body)
	}

	// Verify the task is cancelled.
	taskResp, err := http.Get(ts.URL + "/v1/tasks/" + taskID)
	if err != nil {
		t.Fatalf("GET /v1/tasks/%s failed: %v", taskID, err)
	}
	defer func() { _ = taskResp.Body.Close() }()

	var task map[string]any
	_ = json.NewDecoder(taskResp.Body).Decode(&task)

	status, _ := task["status"].(string)
	if status != "cancelled" {
		t.Fatalf("expected task status %q, got %q", "cancelled", status)
	}
}

func TestAsyncEndpointAgentNotFound(t *testing.T) {
	_, ts := newTestService(t)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/agents/nonexistent/async", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/agents/nonexistent/async failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestGetTaskNotFoundEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/tasks/nonexistent")
	if err != nil {
		t.Fatalf("GET /v1/tasks/nonexistent failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestListToolsEndpoint(t *testing.T) {
	_, ts := newTestService(t)

	resp, err := http.Get(ts.URL + "/v1/tools")
	if err != nil {
		t.Fatalf("GET /v1/tools failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var tools []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	if tools[0]["name"] != "test_tool" {
		t.Fatalf("expected tool name %q, got %q", "test_tool", tools[0]["name"])
	}
}

func TestListToolsNoRegistryEndpoint(t *testing.T) {
	svc := service.New(service.Config{Addr: ":0"})
	ts := httptest.NewServer(svc.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/tools")
	if err != nil {
		t.Fatalf("GET /v1/tools failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var tools []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}
