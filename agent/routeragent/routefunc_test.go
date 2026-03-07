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

package routeragent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/schema"
)

func testRoutes() []Route {
	return []Route{
		{Agent: makeAgent("a", "-A"), Description: "weather"},
		{Agent: makeAgent("b", "-B"), Description: "calendar"},
		{Agent: makeAgent("c", "-C"), Description: "email"},
	}
}

// --- mockChatCompleter ---

type mockChatCompleter struct {
	response *aimodel.ChatResponse
	err      error
	captured *aimodel.ChatRequest
}

func (m *mockChatCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.captured = req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented")
}

func llmResponse(text string, prompt, completion, total int) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
	}
}

// --- FirstFunc ---

func TestFirstFunc(t *testing.T) {
	routes := testRoutes()
	got, err := FirstFunc(context.Background(), &schema.RunRequest{}, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "a" {
		t.Errorf("got agent %q, want %q", got.Agent.ID(), "a")
	}
}

func TestFirstFunc_EmptyRoutes(t *testing.T) {
	_, err := FirstFunc(context.Background(), &schema.RunRequest{}, nil)
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("error = %q, want containing 'no routes'", err.Error())
	}
}

// --- IndexFunc ---

func TestIndexFunc(t *testing.T) {
	routes := testRoutes()
	for i, want := range []string{"a", "b", "c"} {
		fn := IndexFunc(i)
		got, err := fn(context.Background(), &schema.RunRequest{}, routes)
		if err != nil {
			t.Fatalf("index %d: unexpected error: %v", i, err)
		}
		if got.Agent.ID() != want {
			t.Errorf("index %d: got %q, want %q", i, got.Agent.ID(), want)
		}
	}
}

func TestIndexFunc_OutOfRange(t *testing.T) {
	routes := testRoutes()
	for _, i := range []int{-1, 3, 100} {
		fn := IndexFunc(i)
		_, err := fn(context.Background(), &schema.RunRequest{}, routes)
		if err == nil {
			t.Fatalf("index %d: expected error", i)
		}
		if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("index %d: error = %q, want containing 'out of range'", i, err.Error())
		}
	}
}

// --- KeywordFunc ---

func TestKeywordFunc(t *testing.T) {
	routes := testRoutes()
	fn := KeywordFunc(-1) // no fallback
	tests := []struct {
		input string
		want  string
	}{
		{"What's the weather today?", "a"},
		{"Check my CALENDAR", "b"},
		{"Send an email please", "c"},
	}
	for _, tt := range tests {
		req := &schema.RunRequest{
			Messages: []schema.Message{schema.NewUserMessage(tt.input)},
		}
		got, err := fn(context.Background(), req, routes)
		if err != nil {
			t.Fatalf("input %q: unexpected error: %v", tt.input, err)
		}
		if got.Agent.ID() != tt.want {
			t.Errorf("input %q: got %q, want %q", tt.input, got.Agent.ID(), tt.want)
		}
	}
}

func TestKeywordFunc_UsesLastMessage(t *testing.T) {
	routes := testRoutes()
	fn := KeywordFunc(-1)
	req := &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage("weather"),
			schema.NewUserMessage("send email"),
		},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "c" {
		t.Errorf("got %q, want %q (should match last message)", got.Agent.ID(), "c")
	}
}

func TestKeywordFunc_NoMatch(t *testing.T) {
	routes := testRoutes()
	fn := KeywordFunc(-1)
	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("play music")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error for no match")
	}
	if !strings.Contains(err.Error(), "no route matched") {
		t.Errorf("error = %q, want containing 'no route matched'", err.Error())
	}
}

func TestKeywordFunc_EmptyRoutes(t *testing.T) {
	fn := KeywordFunc(-1)
	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	}
	_, err := fn(context.Background(), req, nil)
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
}

func TestKeywordFunc_Fallback(t *testing.T) {
	routes := testRoutes()
	fn := KeywordFunc(0) // fallback to first route
	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("play music")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "a" {
		t.Errorf("got %q, want %q (should fallback to first route)", got.Agent.ID(), "a")
	}
}

func TestKeywordFunc_FallbackOutOfRange(t *testing.T) {
	routes := testRoutes()
	fn := KeywordFunc(99) // out of range fallback
	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("play music")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error for out-of-range fallback")
	}
	if !strings.Contains(err.Error(), "no route matched") {
		t.Errorf("error = %q, want containing 'no route matched'", err.Error())
	}
}

// --- RandomFunc ---

func TestRandomFunc(t *testing.T) {
	routes := testRoutes()
	ids := map[string]bool{}
	for range 100 {
		got, err := RandomFunc(context.Background(), &schema.RunRequest{}, routes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ids[got.Agent.ID()] = true
	}
	// With 100 iterations and 3 routes, all should be selected at least once.
	for _, id := range []string{"a", "b", "c"} {
		if !ids[id] {
			t.Errorf("route %q was never selected in 100 iterations", id)
		}
	}
}

func TestRandomFunc_SingleRoute(t *testing.T) {
	routes := []Route{{Agent: makeAgent("only", ""), Description: "single"}}
	got, err := RandomFunc(context.Background(), &schema.RunRequest{}, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "only" {
		t.Errorf("got %q, want %q", got.Agent.ID(), "only")
	}
}

func TestRandomFunc_EmptyRoutes(t *testing.T) {
	_, err := RandomFunc(context.Background(), &schema.RunRequest{}, nil)
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("error = %q, want containing 'no routes'", err.Error())
	}
}

// --- LLMFunc ---

func TestLLMFunc_SelectsCorrectRoute(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("1", 10, 5, 15)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("check my schedule")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "b" {
		t.Errorf("got agent %q, want %q", got.Agent.ID(), "b")
	}
	// Verify usage is returned.
	if got.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if got.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", got.Usage.PromptTokens)
	}
}

func TestLLMFunc_SelectsFirstRoute(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("0", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("what's the weather?")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "a" {
		t.Errorf("got agent %q, want %q", got.Agent.ID(), "a")
	}
}

func TestLLMFunc_SelectsLastRoute(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("2", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("send email")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "c" {
		t.Errorf("got agent %q, want %q", got.Agent.ID(), "c")
	}
}

func TestLLMFunc_PromptContainsDescriptions(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("0", 5, 3, 8)}
	fn := LLMFunc(mock, "my-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test input")},
	}
	_, _ = fn(context.Background(), req, routes)

	if mock.captured == nil {
		t.Fatal("expected captured request")
	}
	if mock.captured.Model != "my-model" {
		t.Errorf("Model = %q, want %q", mock.captured.Model, "my-model")
	}
	sysText := mock.captured.Messages[0].Content.Text()
	for _, desc := range []string{"weather", "calendar", "email"} {
		if !strings.Contains(sysText, desc) {
			t.Errorf("system prompt missing description %q", desc)
		}
	}
	userText := mock.captured.Messages[1].Content.Text()
	if userText != "test input" {
		t.Errorf("user text = %q, want %q", userText, "test input")
	}
}

func TestLLMFunc_LLMError_NoFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{err: errors.New("LLM unavailable")}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "LLM routing") {
		t.Errorf("error = %q, want containing 'LLM routing'", err.Error())
	}
}

func TestLLMFunc_LLMError_WithFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{err: errors.New("LLM unavailable")}
	fn := LLMFunc(mock, "test-model", 0) // fallback to first

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "a" {
		t.Errorf("got %q, want %q (fallback)", got.Agent.ID(), "a")
	}
}

func TestLLMFunc_NonNumericResponse_NoFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("weather agent", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "non-numeric") {
		t.Errorf("error = %q, want containing 'non-numeric'", err.Error())
	}
}

func TestLLMFunc_NonNumericResponse_WithFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("not a number", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", 2) // fallback to email

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "c" {
		t.Errorf("got %q, want %q (fallback)", got.Agent.ID(), "c")
	}
	if got.Usage == nil {
		t.Fatal("expected usage even on fallback")
	}
}

func TestLLMFunc_OutOfRangeIndex_NoFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("99", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %q, want containing 'out of range'", err.Error())
	}
}

func TestLLMFunc_OutOfRangeIndex_WithFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("99", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", 1) // fallback to calendar

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "b" {
		t.Errorf("got %q, want %q (fallback)", got.Agent.ID(), "b")
	}
}

func TestLLMFunc_NegativeIndex_NoFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("-1", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %q, want containing 'out of range'", err.Error())
	}
}

func TestLLMFunc_EmptyRoutes(t *testing.T) {
	mock := &mockChatCompleter{response: llmResponse("0", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("error = %q, want containing 'no routes'", err.Error())
	}
}

func TestLLMFunc_EmptyChoices_NoFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: &aimodel.ChatResponse{
		Choices: []aimodel.Choice{},
		Usage:   aimodel.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	_, err := fn(context.Background(), req, routes)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty choices") {
		t.Errorf("error = %q, want containing 'empty choices'", err.Error())
	}
}

func TestLLMFunc_EmptyChoices_WithFallback(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: &aimodel.ChatResponse{
		Choices: []aimodel.Choice{},
		Usage:   aimodel.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}}
	fn := LLMFunc(mock, "test-model", 0)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "a" {
		t.Errorf("got %q, want %q (fallback)", got.Agent.ID(), "a")
	}
}

func TestLLMFunc_WhitespaceResponse(t *testing.T) {
	routes := testRoutes()
	mock := &mockChatCompleter{response: llmResponse("  1  \n", 5, 3, 8)}
	fn := LLMFunc(mock, "test-model", -1)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	}
	got, err := fn(context.Background(), req, routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.ID() != "b" {
		t.Errorf("got %q, want %q", got.Agent.ID(), "b")
	}
}
