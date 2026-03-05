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
	"testing"
	"time"

	"github.com/vogo/aimodel"
)

// mockCompleter records calls and returns a configurable response.
type mockCompleter struct {
	chatCalls   int
	streamCalls int
	chatResp    *aimodel.ChatResponse
	chatErr     error
	streamResp  *aimodel.Stream
	streamErr   error
}

func (m *mockCompleter) ChatCompletion(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.chatCalls++
	return m.chatResp, m.chatErr
}

func (m *mockCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	m.streamCalls++
	return m.streamResp, m.streamErr
}

func TestChainEmpty(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "test"}}
	wrapped := Chain(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "test" {
		t.Fatalf("expected ID 'test', got %q", resp.ID)
	}

	if mock.chatCalls != 1 {
		t.Fatalf("expected 1 call, got %d", mock.chatCalls)
	}
}

func TestChainOrder(t *testing.T) {
	var order []string

	mkMiddleware := func(name string) Middleware {
		return MiddlewareFunc(func(next aimodel.ChatCompleter) aimodel.ChatCompleter {
			return &completerFunc{
				chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
					order = append(order, name+"-before")
					resp, err := next.ChatCompletion(ctx, req)
					order = append(order, name+"-after")

					return resp, err
				},
				stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
					return next.ChatCompletionStream(ctx, req)
				},
			}
		})
	}

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	wrapped := Chain(mock, mkMiddleware("A"), mkMiddleware("B"), mkMiddleware("C"))

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"A-before", "B-before", "C-before", "C-after", "B-after", "A-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d entries, got %d: %v", len(expected), len(order), order)
	}

	for i, v := range expected {
		if order[i] != v {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMiddlewareFunc(t *testing.T) {
	called := false
	mw := MiddlewareFunc(func(next aimodel.ChatCompleter) aimodel.ChatCompleter {
		called = true
		return next
	})

	mock := &mockCompleter{}
	mw.Wrap(mock)

	if !called {
		t.Fatal("MiddlewareFunc.Wrap was not called")
	}
}

func TestDefaultChain_AllMiddlewares(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	wrapped := DefaultChain(mock,
		NewLogMiddleware(),
		NewCircuitBreakerMiddleware(),
		NewRateLimitMiddleware(),
		NewRetryMiddleware(withSleepFn(noSleep)),
		NewTimeoutMiddleware(5*time.Second),
		NewCacheMiddleware(NewMapCache()),
	)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}
}

func TestDefaultChain_NilMiddlewares(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	wrapped := DefaultChain(mock, nil, nil, nil)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}
}
