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
	"testing"

	"github.com/vogo/aimodel"
)

func TestBudgetMiddlewarePreCheckBlocksCall(t *testing.T) {
	sentinel := errors.New("budget exceeded: session tokens 100/100")
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "should-not-reach"}}

	mw := NewBudgetMiddleware(
		func(_ context.Context) error { return sentinel },
		func(_ context.Context, _ aimodel.Usage) { t.Fatal("postRecord must not run when preCheck fails") },
	)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("preCheck error should propagate, got %v", err)
	}
	if mock.chatCalls != 0 {
		t.Fatalf("next.ChatCompletion must not run, got %d calls", mock.chatCalls)
	}
}

func TestBudgetMiddlewarePostRecordFires(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{
		ID:    "ok",
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}

	var recorded aimodel.Usage
	mw := NewBudgetMiddleware(
		nil,
		func(_ context.Context, u aimodel.Usage) { recorded = u },
	)
	wrapped := mw.Wrap(mock)

	if _, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recorded.PromptTokens != 10 || recorded.CompletionTokens != 5 {
		t.Fatalf("postRecord received unexpected usage: %+v", recorded)
	}
}

func TestBudgetMiddlewareTransparentWhenNilClosures(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}

	mw := NewBudgetMiddleware(nil, nil)
	wrapped := mw.Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}
	if mock.chatCalls != 1 {
		t.Fatalf("expected 1 call, got %d", mock.chatCalls)
	}
}

func TestBudgetMiddlewareStreamPreCheckBlocks(t *testing.T) {
	sentinel := errors.New("budget exceeded")
	mock := &mockCompleter{streamResp: nil, streamErr: nil}

	var postRecordHit bool
	mw := NewBudgetMiddleware(
		func(_ context.Context) error { return sentinel },
		func(_ context.Context, _ aimodel.Usage) { postRecordHit = true },
	)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("stream preCheck error should propagate, got %v", err)
	}
	if mock.streamCalls != 0 {
		t.Fatalf("next.ChatCompletionStream must not run, got %d calls", mock.streamCalls)
	}
	if postRecordHit {
		t.Fatal("postRecord must not run when preCheck fails")
	}
}

func TestBudgetMiddlewareStreamPassesThroughWhenUpstreamNil(t *testing.T) {
	// WrapStream(nil, cb) invokes the callback immediately with nil usage and
	// returns nil. The middleware must therefore not crash and must leave
	// postRecord responsible for nil-safe behavior.
	mock := &mockCompleter{streamResp: nil, streamErr: nil}

	var hits int
	mw := NewBudgetMiddleware(
		nil,
		func(_ context.Context, _ aimodel.Usage) { hits++ },
	)
	wrapped := mw.Wrap(mock)

	s, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected stream err: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil stream from nil upstream, got %v", s)
	}
	// postRecord should have been skipped — WrapStream's immediate nil-usage
	// callback is intercepted by the middleware's nil guard inside the closure.
	if hits != 0 {
		t.Fatalf("postRecord with nil usage should be skipped, got %d hits", hits)
	}
}
