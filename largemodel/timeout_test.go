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

func TestTimeoutMiddleware_ChatCompletion_Success(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	wrapped := NewTimeoutMiddleware(5 * time.Second).Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected ID 'ok', got %q", resp.ID)
	}
}

func TestTimeoutMiddleware_ChatCompletion_Timeout(t *testing.T) {
	slow := &completerFunc{
		chat: func(ctx context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return &aimodel.ChatResponse{}, nil
			}
		},
		stream: func(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
			return nil, nil
		},
	}

	wrapped := NewTimeoutMiddleware(50 * time.Millisecond).Wrap(slow)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// TestTimeoutMiddleware_Stream_Passthrough verifies that the timeout middleware
// passes the caller's context to ChatCompletionStream unchanged, without
// wrapping it in a deadline that would cancel the stream on return.
func TestTimeoutMiddleware_Stream_Passthrough(t *testing.T) {
	mock := &mockCompleter{streamResp: nil, streamErr: nil}
	wrapped := NewTimeoutMiddleware(50 * time.Millisecond).Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.streamCalls != 1 {
		t.Fatalf("expected ChatCompletionStream to be called exactly once, got %d", mock.streamCalls)
	}
}
