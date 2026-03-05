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
	"io"
	"net"
	"testing"
	"time"

	"github.com/vogo/aimodel"
)

// noSleep is a test sleep function that returns immediately.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

// failingCompleter fails N times then succeeds.
type failingCompleter struct {
	failCount int
	calls     int
	failErr   error
	resp      *aimodel.ChatResponse
}

func (f *failingCompleter) ChatCompletion(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	f.calls++
	if f.calls <= f.failCount {
		return nil, f.failErr
	}

	return f.resp, nil
}

func (f *failingCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, nil
}

func TestRetryMiddleware_SuccessNoRetry(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	wrapped := NewRetryMiddleware(withSleepFn(noSleep)).Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected ID 'ok', got %q", resp.ID)
	}

	if mock.chatCalls != 1 {
		t.Fatalf("expected 1 call, got %d", mock.chatCalls)
	}
}

func TestRetryMiddleware_RetryOnRetryableError(t *testing.T) {
	retryableErr := &aimodel.APIError{StatusCode: 429, Message: "rate limited"}
	fc := &failingCompleter{
		failCount: 2,
		failErr:   retryableErr,
		resp:      &aimodel.ChatResponse{ID: "recovered"},
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), withSleepFn(noSleep)).Wrap(fc)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "recovered" {
		t.Fatalf("expected ID 'recovered', got %q", resp.ID)
	}

	if fc.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", fc.calls)
	}
}

func TestRetryMiddleware_NoRetryOnNonRetryableError(t *testing.T) {
	nonRetryableErr := &aimodel.APIError{StatusCode: 400, Message: "bad request"}
	fc := &failingCompleter{
		failCount: 5,
		failErr:   nonRetryableErr,
		resp:      &aimodel.ChatResponse{ID: "never"},
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}

	if fc.calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", fc.calls)
	}
}

func TestRetryMiddleware_ExhaustedRetries(t *testing.T) {
	retryableErr := &aimodel.APIError{StatusCode: 503, Message: "service unavailable"}
	fc := &failingCompleter{
		failCount: 10,
		failErr:   retryableErr,
		resp:      &aimodel.ChatResponse{ID: "never"},
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(2), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}

	// 1 initial + 2 retries = 3
	if fc.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", fc.calls)
	}
}

// failingStreamCompleter fails stream creation N times then succeeds.
type failingStreamCompleter struct {
	failCount int
	calls     int
	failErr   error
}

func (f *failingStreamCompleter) ChatCompletion(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	return nil, nil
}

func (f *failingStreamCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	f.calls++
	if f.calls <= f.failCount {
		return nil, f.failErr
	}

	return nil, nil // nil stream is acceptable for testing creation retry
}

func TestRetryMiddleware_StreamRetry(t *testing.T) {
	retryableErr := &aimodel.APIError{StatusCode: 429, Message: "rate limited"}
	fc := &failingStreamCompleter{
		failCount: 2,
		failErr:   retryableErr,
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 2 failures + 1 success = 3 calls
	if fc.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", fc.calls)
	}
}

func TestRetryMiddleware_StreamNoRetryOnNonRetryable(t *testing.T) {
	nonRetryableErr := &aimodel.APIError{StatusCode: 400, Message: "bad request"}
	fc := &failingStreamCompleter{
		failCount: 5,
		failErr:   nonRetryableErr,
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}

	if fc.calls != 1 {
		t.Fatalf("expected 1 call (no retry on 400), got %d", fc.calls)
	}
}

func TestRetryMiddleware_StreamExhaustedRetries(t *testing.T) {
	retryableErr := &aimodel.APIError{StatusCode: 503, Message: "service unavailable"}
	fc := &failingStreamCompleter{
		failCount: 10,
		failErr:   retryableErr,
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(2), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}

	// 1 initial + 2 retries = 3
	if fc.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", fc.calls)
	}
}

func TestRetryMiddleware_NonAPIError(t *testing.T) {
	// A plain errors.New value is not a *aimodel.APIError, net.Error, or io.EOF,
	// so it must not be retried.
	fc := &failingCompleter{
		failCount: 5,
		failErr:   errors.New("permission denied"),
		resp:      &aimodel.ChatResponse{},
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), withSleepFn(noSleep)).Wrap(fc)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}

	if fc.calls != 1 {
		t.Fatalf("expected 1 call (plain errors are not retryable), got %d", fc.calls)
	}
}

func TestRetryMiddleware_NetworkErrorRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name: "net.OpError with timeout",
			err: &net.OpError{
				Op:  "dial",
				Err: &timeoutError{},
			},
			retryable: true,
		},
		{
			name:      "io.EOF",
			err:       io.EOF,
			retryable: true,
		},
		{
			name:      "io.ErrUnexpectedEOF",
			err:       io.ErrUnexpectedEOF,
			retryable: true,
		},
		{
			name:      "plain errors.New",
			err:       errors.New("unknown"),
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.retryable {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

// timeoutError is a net.Error that reports Timeout() == true.
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestRetryMiddleware_ContextCancelled(t *testing.T) {
	retryableErr := &aimodel.APIError{StatusCode: 500, Message: "server error"}
	fc := &failingCompleter{
		failCount: 10,
		failErr:   retryableErr,
		resp:      &aimodel.ChatResponse{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	sleepFn := func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(5), withSleepFn(sleepFn)).Wrap(fc)

	_, err := wrapped.ChatCompletion(ctx, &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryMiddleware_BackoffCapped(t *testing.T) {
	m := NewRetryMiddleware(
		WithBaseDelay(1*time.Second),
		WithMaxDelay(5*time.Second),
	)

	d := m.backoffStrategy.Delay(10)
	// With jitter, max is 5s + 25% = 6.25s
	if d > 7*time.Second {
		t.Fatalf("backoff too large: %v", d)
	}

	if d < 5*time.Second {
		t.Fatalf("backoff should be at least maxDelay: %v", d)
	}
}

func TestExponentialBackoff_Delay(t *testing.T) {
	b := &ExponentialBackoff{
		BaseDelay: 1 * time.Second,
		MaxDelay:  10 * time.Second,
		Jitter:    0, // zero jitter for deterministic testing
	}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 10 * time.Second},  // capped
		{10, 10 * time.Second}, // still capped
	}

	for _, tt := range tests {
		got := b.Delay(tt.attempt)
		if got != tt.expected {
			t.Errorf("Delay(%d) = %v, want %v", tt.attempt, got, tt.expected)
		}
	}
}

func TestRetryMiddleware_WithCustomBackoff(t *testing.T) {
	// Fixed backoff that always returns 0 (no delay).
	fixed := &fixedBackoff{}
	retryableErr := &aimodel.APIError{StatusCode: 429, Message: "rate limited"}
	fc := &failingCompleter{
		failCount: 2,
		failErr:   retryableErr,
		resp:      &aimodel.ChatResponse{ID: "recovered"},
	}

	wrapped := NewRetryMiddleware(WithMaxRetries(3), WithBackoff(fixed)).Wrap(fc)
	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "recovered" {
		t.Fatalf("expected 'recovered', got %q", resp.ID)
	}

	if fc.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", fc.calls)
	}
}

type fixedBackoff struct{}

func (f *fixedBackoff) Delay(_ int) time.Duration { return 0 }

func TestRetryMiddleware_RetryableStatusCodes(t *testing.T) {
	tests := []struct {
		code      int
		retryable bool
	}{
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
	}

	for _, tt := range tests {
		err := &aimodel.APIError{StatusCode: tt.code}
		if got := isRetryable(err); got != tt.retryable {
			t.Errorf("isRetryable(status %d) = %v, want %v", tt.code, got, tt.retryable)
		}
	}
}
