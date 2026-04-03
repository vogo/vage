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

package metrics_tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
)

// Test 2a: MetricsMiddleware propagates CacheReadTokens in sync path.
// Creates a MetricsMiddleware wrapping a mock ChatCompleter returning fixed
// usage with CacheReadTokens, and verifies dispatched LLMCallEndData includes it.
func TestIntegration_MetricsMiddleware_CacheReadTokens_Sync(t *testing.T) {
	// Set up mock server returning usage with cache tokens via OpenAI protocol.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"total_tokens": 150,
				"prompt_tokens_details": {
					"cached_tokens": 25
				}
			}
		}`))
	}))
	defer srv.Close()

	client, err := aimodel.NewClient(
		aimodel.WithAPIKey("sk-test"),
		aimodel.WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mw := largemodel.NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(client)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4o",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	// Verify the response has CacheReadTokens.
	if resp.Usage.CacheReadTokens != 25 {
		t.Errorf("resp.Usage.CacheReadTokens = %d, want 25", resp.Usage.CacheReadTokens)
	}

	// Verify events contain CacheReadTokens.
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Fatalf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if endData.CacheReadTokens != 25 {
		t.Errorf("endData.CacheReadTokens = %d, want 25", endData.CacheReadTokens)
	}

	if endData.PromptTokens != 100 {
		t.Errorf("endData.PromptTokens = %d, want 100", endData.PromptTokens)
	}

	if endData.CompletionTokens != 50 {
		t.Errorf("endData.CompletionTokens = %d, want 50", endData.CompletionTokens)
	}
}

// Test 2b: MetricsMiddleware propagates CacheReadTokens in stream path.
// Creates a MetricsMiddleware wrapping a real OpenAI-protocol stream that
// includes cached_tokens and verifies the dispatched LLMCallEndData on close.
func TestIntegration_MetricsMiddleware_CacheReadTokens_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":25}}}`,
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}

		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client, err := aimodel.NewClient(
		aimodel.WithAPIKey("sk-test"),
		aimodel.WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mw := largemodel.NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(client)

	stream, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4o",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	// Only EventLLMCallStart before drain.
	if len(events) != 1 {
		t.Fatalf("expected 1 event before drain, got %d", len(events))
	}

	// Drain stream.
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

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if !endData.Stream {
		t.Error("endData.Stream should be true")
	}

	if endData.CacheReadTokens != 25 {
		t.Errorf("endData.CacheReadTokens = %d, want 25", endData.CacheReadTokens)
	}

	if endData.PromptTokens != 100 {
		t.Errorf("endData.PromptTokens = %d, want 100", endData.PromptTokens)
	}

	if endData.CompletionTokens != 50 {
		t.Errorf("endData.CompletionTokens = %d, want 50", endData.CompletionTokens)
	}
}

// Test 2c: MetricsMiddleware with zero CacheReadTokens.
// Verifies that CacheReadTokens is zero when there are no cached tokens.
func TestIntegration_MetricsMiddleware_ZeroCacheReadTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"total_tokens": 150
			}
		}`))
	}))
	defer srv.Close()

	client, err := aimodel.NewClient(
		aimodel.WithAPIKey("sk-test"),
		aimodel.WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mw := largemodel.NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(client)

	_, err = wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4o",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	endData := events[1].Data.(schema.LLMCallEndData)
	if endData.CacheReadTokens != 0 {
		t.Errorf("endData.CacheReadTokens = %d, want 0", endData.CacheReadTokens)
	}
}
