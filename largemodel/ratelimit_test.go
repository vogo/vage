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
	"time"

	"github.com/vogo/aimodel"
)

func TestRateLimitMiddleware_RequestsPerMin(t *testing.T) {
	now := time.Now()
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{}}

	wrapped := NewRateLimitMiddleware(
		WithRequestsPerMin(2),
		withNowFn(func() time.Time { return now }),
	).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// First two should succeed.
	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}

	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 2: unexpected error: %v", err)
	}

	// Third should be rate limited.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("call 3: expected ErrRateLimited, got %v", err)
	}
}

func TestRateLimitMiddleware_WindowSlides(t *testing.T) {
	now := time.Now()
	currentTime := now

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{}}

	wrapped := NewRateLimitMiddleware(
		WithRequestsPerMin(1),
		withNowFn(func() time.Time { return currentTime }),
	).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}

	// Should be rate limited.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("call 2: expected ErrRateLimited, got %v", err)
	}

	// Advance past the window.
	currentTime = now.Add(61 * time.Second)

	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 3 after window: unexpected error: %v", err)
	}
}

func TestRateLimitMiddleware_TokensPerMin(t *testing.T) {
	now := time.Now()
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{
		Usage: aimodel.Usage{TotalTokens: 600},
	}}

	wrapped := NewRateLimitMiddleware(
		WithTokensPerMin(1000),
		withNowFn(func() time.Time { return now }),
	).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// First call: 600 tokens used.
	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}

	// Second call: 600+600=1200 > 1000, should be limited after recording.
	// But rate limit check happens before the call, so this should succeed.
	if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("call 2: unexpected error: %v", err)
	}

	// Third call: now 1200 tokens recorded, exceeds 1000.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("call 3: expected ErrRateLimited, got %v", err)
	}
}

func TestRateLimitMiddleware_StreamRateLimit(t *testing.T) {
	now := time.Now()
	mock := &mockCompleter{}

	wrapped := NewRateLimitMiddleware(
		WithRequestsPerMin(1),
		withNowFn(func() time.Time { return now }),
	).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	if _, err := wrapped.ChatCompletionStream(ctx, req); err != nil {
		t.Fatalf("stream call 1: unexpected error: %v", err)
	}

	_, err := wrapped.ChatCompletionStream(ctx, req)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("stream call 2: expected ErrRateLimited, got %v", err)
	}
}

func TestRateLimitMiddleware_NoLimits(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{}}
	wrapped := NewRateLimitMiddleware().Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	for range 100 {
		if _, err := wrapped.ChatCompletion(ctx, req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
