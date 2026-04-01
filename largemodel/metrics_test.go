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

package largemodel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

func TestMetricsMiddleware_ChatCompletion(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{
		ID:    "ok",
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}

	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	startData, ok := events[0].Data.(schema.LLMCallStartData)
	if !ok {
		t.Fatalf("events[0].Data type = %T, want LLMCallStartData", events[0].Data)
	}

	if startData.Model != "gpt-4" {
		t.Errorf("startData.Model = %q, want %q", startData.Model, "gpt-4")
	}

	if startData.Messages != 1 {
		t.Errorf("startData.Messages = %d, want 1", startData.Messages)
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if endData.TotalTokens != 15 {
		t.Errorf("endData.TotalTokens = %d, want 15", endData.TotalTokens)
	}
}

func TestMetricsMiddleware_ChatCompletion_Error(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{chatErr: errors.New("API error")}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[1].Type != schema.EventLLMCallError {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallError)
	}

	errData, ok := events[1].Data.(schema.LLMCallErrorData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallErrorData", events[1].Data)
	}

	if errData.Error != "API error" {
		t.Errorf("errData.Error = %q, want %q", errData.Error, "API error")
	}
}

func TestMetricsMiddleware_Stream_Error(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{streamErr: errors.New("stream error")}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	startData, ok := events[0].Data.(schema.LLMCallStartData)
	if !ok {
		t.Fatalf("events[0].Data type = %T, want LLMCallStartData", events[0].Data)
	}

	if !startData.Stream {
		t.Error("startData.Stream should be true")
	}

	if events[1].Type != schema.EventLLMCallError {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallError)
	}

	errData, ok := events[1].Data.(schema.LLMCallErrorData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallErrorData", events[1].Data)
	}

	if !errData.Stream {
		t.Error("errData.Stream should be true")
	}
}

func TestMetricsMiddleware_Stream_Success(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if !endData.Stream {
		t.Error("endData.Stream should be true")
	}
}

func TestMetricsMiddleware_Stream_CloseEmitsEndWithUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}

		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := aimodel.NewClient(aimodel.WithAPIKey("sk-test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(c)

	stream, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4o",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	// Only EventLLMCallStart should have fired so far.
	if len(events) != 1 {
		t.Fatalf("expected 1 event before close, got %d", len(events))
	}

	// Drain the stream to populate usage.
	for {
		_, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
	}

	_ = stream.Close()

	// Now EventLLMCallEnd should have been emitted.
	if len(events) != 2 {
		t.Fatalf("expected 2 events after close, got %d", len(events))
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if !endData.Stream {
		t.Error("endData.Stream should be true")
	}

	if endData.PromptTokens != 10 {
		t.Errorf("endData.PromptTokens = %d, want 10", endData.PromptTokens)
	}

	if endData.CompletionTokens != 5 {
		t.Errorf("endData.CompletionTokens = %d, want 5", endData.CompletionTokens)
	}

	if endData.TotalTokens != 15 {
		t.Errorf("endData.TotalTokens = %d, want 15", endData.TotalTokens)
	}
}

func TestMetricsMiddleware_Stream_CloseEmitsEndWithoutUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Stream without usage data in any chunk.
		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}

		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := aimodel.NewClient(aimodel.WithAPIKey("sk-test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(c)

	stream, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4o",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	for {
		_, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
	}

	_ = stream.Close()

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if !endData.Stream {
		t.Error("endData.Stream should be true")
	}

	// Without usage in stream, tokens should be zero.
	if endData.PromptTokens != 0 {
		t.Errorf("endData.PromptTokens = %d, want 0", endData.PromptTokens)
	}

	if endData.TotalTokens != 0 {
		t.Errorf("endData.TotalTokens = %d, want 0", endData.TotalTokens)
	}
}

func TestMetricsMiddleware_NilDispatch(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil dispatch")
		}
	}()

	NewMetricsMiddleware(nil)
}
