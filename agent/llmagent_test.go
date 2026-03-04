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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

// mockChatCompleter implements aimodel.ChatCompleter for testing.
type mockChatCompleter struct {
	calls     int
	responses []*aimodel.ChatResponse
	err       error
	requests  []*aimodel.ChatRequest // captured requests
}

func (m *mockChatCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	if m.calls >= len(m.responses) {
		return nil, errors.New("mock: no more responses")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func (m *mockChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented")
}

func stopResponse(text string) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

func toolCallResponse(toolCallID, funcName, args string) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message: aimodel.Message{
				Role:    aimodel.RoleAssistant,
				Content: aimodel.NewTextContent(""),
				ToolCalls: []aimodel.ToolCall{{
					ID:       toolCallID,
					Type:     "function",
					Function: aimodel.FunctionCall{Name: funcName, Arguments: args},
				}},
			},
			FinishReason: aimodel.FinishReasonToolCalls,
		}},
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

// --- Tests ---

func TestNewLLMAgent_Defaults(t *testing.T) {
	a := NewLLMAgent(Config{})
	if a.maxIterations != defaultMaxIterations {
		t.Errorf("maxIterations = %d, want %d", a.maxIterations, defaultMaxIterations)
	}
	if a.streamBufferSize != defaultStreamBufferSize {
		t.Errorf("streamBufferSize = %d, want %d", a.streamBufferSize, defaultStreamBufferSize)
	}
	if a.ID() != "" {
		t.Errorf("ID = %q, want empty", a.ID())
	}
	if a.Name() != "" {
		t.Errorf("Name = %q, want empty", a.Name())
	}
	if a.Tools() != nil {
		t.Error("Tools should be nil without registry")
	}
}

func TestNewLLMAgent_WithOptions(t *testing.T) {
	a := NewLLMAgent(
		Config{ID: "agent-1", Name: "test-agent", Description: "a test agent"},
		WithModel("gpt-4"),
		WithMaxIterations(5),
		WithMaxTokens(1024),
		WithTemperature(0.7),
		WithStreamBufferSize(64),
	)
	if a.ID() != "agent-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "agent-1")
	}
	if a.Name() != "test-agent" {
		t.Errorf("Name = %q, want %q", a.Name(), "test-agent")
	}
	if a.Description() != "a test agent" {
		t.Errorf("Description = %q, want %q", a.Description(), "a test agent")
	}
	if a.model != "gpt-4" {
		t.Errorf("model = %q, want %q", a.model, "gpt-4")
	}
	if a.maxIterations != 5 {
		t.Errorf("maxIterations = %d, want 5", a.maxIterations)
	}
	if *a.maxTokens != 1024 {
		t.Errorf("maxTokens = %d, want 1024", *a.maxTokens)
	}
	if *a.temperature != 0.7 {
		t.Errorf("temperature = %f, want 0.7", *a.temperature)
	}
	if a.streamBufferSize != 64 {
		t.Errorf("streamBufferSize = %d, want 64", a.streamBufferSize)
	}
}

func TestLLMAgent_Run_NoChatCompleter(t *testing.T) {
	a := NewLLMAgent(Config{})
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected error without ChatCompleter")
	}
	if !strings.Contains(err.Error(), "ChatCompleter is required") {
		t.Errorf("error = %q, want ChatCompleter error", err.Error())
	}
}

func TestLLMAgent_Run_SimpleResponse(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("Hello!")}}
	a := NewLLMAgent(
		Config{ID: "a1"},
		WithChatCompleter(mock),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Content.Text() != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Messages[0].Content.Text(), "Hello!")
	}
	if resp.Messages[0].AgentID != "a1" {
		t.Errorf("AgentID = %q, want %q", resp.Messages[0].AgentID, "a1")
	}
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestLLMAgent_Run_WithSystemPrompt(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}
	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithSystemPrompt(prompt.StringPrompt("You are helpful.")),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Verify system message was prepended.
	req := mock.requests[0]
	if len(req.Messages) < 2 {
		t.Fatalf("len(Messages) = %d, want >= 2", len(req.Messages))
	}
	if req.Messages[0].Role != aimodel.RoleSystem {
		t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, aimodel.RoleSystem)
	}
	if req.Messages[0].Content.Text() != "You are helpful." {
		t.Errorf("system content = %q, want %q", req.Messages[0].Content.Text(), "You are helpful.")
	}
}

func TestLLMAgent_Run_WithTemplateSystemPrompt(t *testing.T) {
	// Go text/template renders missing fields on nil data as "<no value>",
	// so the system prompt renders successfully and is sent to the LLM.
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}
	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithSystemPrompt(prompt.StringPrompt("Hello, {{.User}}!")),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	req := mock.requests[0]
	if req.Messages[0].Role != aimodel.RoleSystem {
		t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, aimodel.RoleSystem)
	}
	want := "Hello, <no value>!"
	if req.Messages[0].Content.Text() != want {
		t.Errorf("system content = %q, want %q", req.Messages[0].Content.Text(), want)
	}
}

func TestLLMAgent_Run_ToolCallLoop(t *testing.T) {
	// First response: tool call. Second response: stop.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "get_weather", `{"city":"Paris"}`),
			stopResponse("The weather in Paris is sunny."),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "get_weather", Description: "Get weather"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			return schema.TextResult("", "sunny, 22°C"), nil
		},
	)

	a := NewLLMAgent(
		Config{ID: "weather-agent"},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("What's the weather in Paris?")},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.Messages[0].Content.Text() != "The weather in Paris is sunny." {
		t.Errorf("final response = %q", resp.Messages[0].Content.Text())
	}

	// Two LLM calls: one returned tool_calls, one returned stop.
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.calls)
	}

	// Usage accumulated across iterations.
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
	}

	// Second request should include tool result message.
	secondReq := mock.requests[1]
	lastMsg := secondReq.Messages[len(secondReq.Messages)-1]
	if lastMsg.Role != aimodel.RoleTool {
		t.Errorf("last message Role = %q, want %q", lastMsg.Role, aimodel.RoleTool)
	}
	if lastMsg.ToolCallID != "tc-1" {
		t.Errorf("ToolCallID = %q, want %q", lastMsg.ToolCallID, "tc-1")
	}
	if lastMsg.Content.Text() != "sunny, 22°C" {
		t.Errorf("tool result content = %q, want %q", lastMsg.Content.Text(), "sunny, 22°C")
	}
}

func TestLLMAgent_Run_ToolExecutionError(t *testing.T) {
	// Tool errors should be fed back to LLM as ErrorResult, not abort the loop.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "failing_tool", "{}"),
			stopResponse("Sorry, the tool failed."),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "failing_tool"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{}, errors.New("connection refused")
		},
	)

	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do something")},
	})
	if err != nil {
		t.Fatalf("Run error: %v (tool errors should not abort the loop)", err)
	}
	if resp.Messages[0].Content.Text() != "Sorry, the tool failed." {
		t.Errorf("response = %q", resp.Messages[0].Content.Text())
	}

	// Verify the error was fed back to LLM.
	secondReq := mock.requests[1]
	lastMsg := secondReq.Messages[len(secondReq.Messages)-1]
	if lastMsg.Content.Text() != "connection refused" {
		t.Errorf("error feedback = %q, want %q", lastMsg.Content.Text(), "connection refused")
	}
}

func TestLLMAgent_Run_MaxIterationsExceeded(t *testing.T) {
	// Always returns tool calls, never stops.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "loop", "{}"),
			toolCallResponse("tc-2", "loop", "{}"),
			toolCallResponse("tc-3", "loop", "{}"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "loop"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "ok"), nil
		},
	)

	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithMaxIterations(2),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("loop forever")},
	})
	if err == nil {
		t.Fatal("expected max iterations error")
	}
	if !strings.Contains(err.Error(), "exceeded max iterations") {
		t.Errorf("error = %q, want max iterations error", err.Error())
	}
}

func TestLLMAgent_Run_ChatCompletionError(t *testing.T) {
	mock := &mockChatCompleter{err: errors.New("API error")}
	a := NewLLMAgent(Config{}, WithChatCompleter(mock))

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "chat completion") {
		t.Errorf("error = %q, want chat completion error", err.Error())
	}
}

func TestLLMAgent_Run_EmptyResponse(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{{Choices: nil, Usage: aimodel.Usage{}}},
	}
	a := NewLLMAgent(Config{}, WithChatCompleter(mock))

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error = %q, want empty response error", err.Error())
	}
}

func TestLLMAgent_Run_OptionsOverride(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}
	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithModel("default-model"),
		WithTemperature(0.5),
	)

	temp := 0.9
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
		Options: &schema.RunOptions{
			Model:       "override-model",
			Temperature: &temp,
			MaxTokens:   2048,
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	req := mock.requests[0]
	if req.Model != "override-model" {
		t.Errorf("Model = %q, want %q", req.Model, "override-model")
	}
	if req.Temperature == nil || *req.Temperature != 0.9 {
		t.Errorf("Temperature = %v, want 0.9", req.Temperature)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", req.MaxTokens)
	}
}

func TestLLMAgent_Run_ToolFilter(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "allowed"}, echoToolHandler)
	_ = reg.Register(schema.ToolDef{Name: "blocked"}, echoToolHandler)

	a := NewLLMAgent(Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
		Options:  &schema.RunOptions{Tools: []string{"allowed"}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	req := mock.requests[0]
	if len(req.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
	}
	if req.Tools[0].Function.Name != "allowed" {
		t.Errorf("Tools[0].Name = %q, want %q", req.Tools[0].Function.Name, "allowed")
	}
}

func TestLLMAgent_Run_SessionIDPassthrough(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}
	a := NewLLMAgent(Config{}, WithChatCompleter(mock))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
		SessionID: "session-123",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.SessionID != "session-123" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "session-123")
	}
}

func TestLLMAgent_Tools_WithRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "t1"}, echoToolHandler)
	a := NewLLMAgent(Config{}, WithToolRegistry(reg))
	tools := a.Tools()
	if len(tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(tools))
	}
	if tools[0].Name != "t1" {
		t.Errorf("Tools[0].Name = %q, want %q", tools[0].Name, "t1")
	}
}

func TestRunText(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("world")}}
	a := NewLLMAgent(Config{}, WithChatCompleter(mock))

	resp, err := RunText(context.Background(), a, "hello")
	if err != nil {
		t.Fatalf("RunText error: %v", err)
	}
	if resp.Messages[0].Content.Text() != "world" {
		t.Errorf("response = %q, want %q", resp.Messages[0].Content.Text(), "world")
	}

	// Verify the user message was sent.
	req := mock.requests[0]
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Content.Text() != "hello" {
		t.Errorf("input = %q, want %q", req.Messages[0].Content.Text(), "hello")
	}
}

func TestLLMAgent_Run_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := &mockChatCompleter{err: ctx.Err()}
	a := NewLLMAgent(Config{}, WithChatCompleter(mock))

	_, err := a.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func echoToolHandler(_ context.Context, name, args string) (schema.ToolResult, error) {
	return schema.TextResult("", name+":"+args), nil
}

// --- Streaming tests ---

// sseStreamServer creates an httptest.Server that serves OpenAI-compatible SSE responses.
// Each call to ChatCompletionStream cycles through the provided response sets.
func sseStreamServer(t *testing.T, responseSets [][]string) *httptest.Server {
	t.Helper()

	callIdx := 0

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req aimodel.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}

		if callIdx >= len(responseSets) {
			http.Error(w, "no more responses", http.StatusInternalServerError)
			return
		}

		chunks := responseSets[callIdx]
		callIdx++

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}

		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// textDeltaChunk returns an SSE JSON chunk with a text content delta.
func textDeltaChunk(text string) string {
	return fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":%s},"finish_reason":null}]}`, mustMarshal(text))
}

// stopChunk returns an SSE JSON chunk with finish_reason=stop.
func stopChunk() string {
	return `{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
}

// toolCallChunks returns SSE JSON chunks that build up a tool call across multiple deltas.
func toolCallChunks(id, name, args string) []string {
	return []string{
		fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]},"finish_reason":null}]}`, mustMarshal(id), mustMarshal(name)),
		fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]},"finish_reason":null}]}`, mustMarshal(args)),
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestLLMAgent_RunStream_SimpleText(t *testing.T) {
	srv := sseStreamServer(t, [][]string{
		{textDeltaChunk("Hello"), textDeltaChunk(" world"), stopChunk()},
	})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	a := NewLLMAgent(
		Config{ID: "test-agent"},
		WithChatCompleter(client),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	var events []schema.Event
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv error: %v", recvErr)
		}
		events = append(events, e)
	}

	// Expect: AgentStart, IterationStart, TextDelta("Hello"), TextDelta(" world"), AgentEnd
	if len(events) < 5 {
		t.Fatalf("got %d events, want >= 5", len(events))
	}

	if events[0].Type != schema.EventAgentStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventAgentStart)
	}
	if events[0].AgentID != "test-agent" {
		t.Errorf("events[0].AgentID = %q, want %q", events[0].AgentID, "test-agent")
	}
	if events[0].SessionID != "sess-1" {
		t.Errorf("events[0].SessionID = %q, want %q", events[0].SessionID, "sess-1")
	}

	if events[1].Type != schema.EventIterationStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventIterationStart)
	}

	// Collect text deltas.
	var text strings.Builder
	for _, e := range events {
		if e.Type == schema.EventTextDelta {
			data, ok := e.Data.(schema.TextDeltaData)
			if !ok {
				t.Fatalf("TextDelta data type = %T", e.Data)
			}
			text.WriteString(data.Delta)
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("accumulated text = %q, want %q", text.String(), "Hello world")
	}

	last := events[len(events)-1]
	if last.Type != schema.EventAgentEnd {
		t.Errorf("last event Type = %q, want %q", last.Type, schema.EventAgentEnd)
	}
	endData, ok := last.Data.(schema.AgentEndData)
	if !ok {
		t.Fatalf("AgentEnd data type = %T", last.Data)
	}
	if endData.Message != "Hello world" {
		t.Errorf("AgentEnd message = %q, want %q", endData.Message, "Hello world")
	}
}

func TestLLMAgent_RunStream_ToolCallLoop(t *testing.T) {
	// First response: tool call. Second response: text.
	tcChunks := toolCallChunks("tc-1", "get_weather", `{"city":"Paris"}`)
	textChunks := []string{textDeltaChunk("Sunny"), stopChunk()}

	srv := sseStreamServer(t, [][]string{tcChunks, textChunks})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "get_weather", Description: "Get weather"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "sunny, 22°C"), nil
		},
	)

	a := NewLLMAgent(
		Config{ID: "weather-agent"},
		WithChatCompleter(client),
		WithToolRegistry(reg),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("Weather?")},
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	var types []string
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv error: %v", recvErr)
		}
		types = append(types, e.Type)
	}

	// Expect: AgentStart, IterationStart(0), ToolCallStart, ToolCallEnd, ToolResult,
	//         IterationStart(1), TextDelta("Sunny"), AgentEnd
	wantTypes := []string{
		schema.EventAgentStart,
		schema.EventIterationStart,
		schema.EventToolCallStart,
		schema.EventToolCallEnd,
		schema.EventToolResult,
		schema.EventIterationStart,
		schema.EventTextDelta,
		schema.EventAgentEnd,
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("event types = %v, want %v", types, wantTypes)
	}
	for i, want := range wantTypes {
		if types[i] != want {
			t.Errorf("types[%d] = %q, want %q", i, types[i], want)
		}
	}
}

func TestLLMAgent_RunStream_CloseEarly(t *testing.T) {
	// Server sends many chunks, but we close early.
	var manyChunks []string
	for range 50 {
		manyChunks = append(manyChunks, textDeltaChunk("x"))
	}
	manyChunks = append(manyChunks, stopChunk())

	srv := sseStreamServer(t, [][]string{manyChunks})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	a := NewLLMAgent(Config{}, WithChatCompleter(client))
	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	// Read just the first event (AgentStart).
	e, err := rs.Recv()
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("first event Type = %q, want %q", e.Type, schema.EventAgentStart)
	}

	// Close early.
	if err := rs.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Subsequent Recv returns ErrRunStreamClosed.
	_, err = rs.Recv()
	if !errors.Is(err, schema.ErrRunStreamClosed) {
		t.Errorf("Recv after close error = %v, want ErrRunStreamClosed", err)
	}
}

func TestLLMAgent_RunStream_MaxIterations(t *testing.T) {
	// Always returns tool calls.
	tcChunks1 := toolCallChunks("tc-1", "loop", "{}")
	tcChunks2 := toolCallChunks("tc-2", "loop", "{}")

	srv := sseStreamServer(t, [][]string{tcChunks1, tcChunks2})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "loop"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "ok"), nil
		},
	)

	a := NewLLMAgent(Config{},
		WithChatCompleter(client),
		WithToolRegistry(reg),
		WithMaxIterations(1),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("loop")},
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	// Drain all events -- the producer should return a max iterations error.
	var lastErr error
	for {
		_, recvErr := rs.Recv()
		if recvErr != nil {
			lastErr = recvErr
			break
		}
	}

	if lastErr == nil {
		t.Fatal("expected max iterations error")
	}
	if !strings.Contains(lastErr.Error(), "exceeded max iterations") {
		t.Errorf("error = %q, want max iterations error", lastErr.Error())
	}
}

func TestLLMAgent_RunStream_NoChatCompleter(t *testing.T) {
	a := NewLLMAgent(Config{})
	_, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected error without ChatCompleter")
	}
	if !strings.Contains(err.Error(), "ChatCompleter is required") {
		t.Errorf("error = %q, want ChatCompleter error", err.Error())
	}
}

func TestRunStreamText(t *testing.T) {
	srv := sseStreamServer(t, [][]string{
		{textDeltaChunk("ok"), stopChunk()},
	})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	a := NewLLMAgent(Config{}, WithChatCompleter(client))
	rs, err := RunStreamText(context.Background(), a, "hello")
	if err != nil {
		t.Fatalf("RunStreamText error: %v", err)
	}

	var types []string
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv error: %v", recvErr)
		}
		types = append(types, e.Type)
	}

	// AgentStart, IterationStart, TextDelta, AgentEnd
	if len(types) != 4 {
		t.Fatalf("got %d events, want 4: %v", len(types), types)
	}
	wantTypes := []string{schema.EventAgentStart, schema.EventIterationStart, schema.EventTextDelta, schema.EventAgentEnd}
	for i, want := range wantTypes {
		if types[i] != want {
			t.Errorf("types[%d] = %q, want %q", i, types[i], want)
		}
	}
}

func TestLLMAgent_RunStream_StreamAgentInterface(t *testing.T) {
	var _ StreamAgent = (*LLMAgent)(nil)
}

func TestLLMAgent_RunStream_Middleware(t *testing.T) {
	srv := sseStreamServer(t, [][]string{
		{textDeltaChunk("hi"), stopChunk()},
	})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32

	countMiddleware := func(next func(schema.Event) error) func(schema.Event) error {
		return func(e schema.Event) error {
			count.Add(1)
			return next(e)
		}
	}

	a := NewLLMAgent(Config{},
		WithChatCompleter(client),
		WithStreamMiddleware(countMiddleware),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	for {
		_, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv error: %v", recvErr)
		}
	}

	// AgentStart + IterationStart + TextDelta + AgentEnd = 4
	if count.Load() != 4 {
		t.Errorf("middleware called %d times, want 4", count.Load())
	}
}

func TestRunToStream(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("hello")}}
	a := NewLLMAgent(Config{ID: "test-agent"}, WithChatCompleter(mock))

	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
		SessionID: "s1",
	}

	rs := RunToStream(context.Background(), a, req)

	var events []schema.Event
	for {
		e, err := rs.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != schema.EventAgentStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventAgentStart)
	}
	if events[0].AgentID != "test-agent" {
		t.Errorf("events[0].AgentID = %q, want %q", events[0].AgentID, "test-agent")
	}
	if events[1].Type != schema.EventAgentEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventAgentEnd)
	}
	endData := events[1].Data.(schema.AgentEndData)
	if endData.Message != "hello" {
		t.Errorf("AgentEnd message = %q, want %q", endData.Message, "hello")
	}
}
