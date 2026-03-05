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

// errFake is a sentinel used to simulate backend failures in circuit breaker tests.
var errFake = errors.New("fake backend error")

// frozenClock returns a function that always returns the given instant and an
// advance helper that moves the clock forward by a duration.
func frozenClock(start time.Time) (nowFn func() time.Time, advance func(time.Duration)) {
	t := start
	nowFn = func() time.Time { return t }
	advance = func(d time.Duration) { t = t.Add(d) }

	return nowFn, advance
}

func TestCircuitBreaker_ClosedPassesThrough(t *testing.T) {
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "ok"}}
	cb := NewCircuitBreakerMiddleware(WithFailureThreshold(3))
	wrapped := cb.Wrap(mock)

	for i := range 5 {
		resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}

		if resp.ID != "ok" {
			t.Fatalf("call %d: expected ID 'ok', got %q", i, resp.ID)
		}
	}

	if mock.chatCalls != 5 {
		t.Fatalf("expected 5 backend calls, got %d", mock.chatCalls)
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	const threshold = 3

	mock := &mockCompleter{chatErr: errFake}
	cb := NewCircuitBreakerMiddleware(WithFailureThreshold(threshold))
	wrapped := cb.Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// Drive the circuit to open by hitting the threshold.
	for i := range threshold {
		_, err := wrapped.ChatCompletion(ctx, req)
		if !errors.Is(err, errFake) {
			t.Fatalf("call %d: expected errFake, got %v", i, err)
		}
	}

	// The next call must be rejected by the open circuit.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after threshold, got %v", err)
	}

	// Backend should not have been called for the rejected request.
	if mock.chatCalls != threshold {
		t.Fatalf("expected %d backend calls, got %d", threshold, mock.chatCalls)
	}
}

func TestCircuitBreaker_HalfOpenAfterTimeout(t *testing.T) {
	const threshold = 2

	nowFn, advance := frozenClock(time.Now())
	resetTimeout := 10 * time.Second

	// Start with a failing backend, then switch to success for the probe.
	failing := true
	custom := &completerFunc{
		chat: func(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			if failing {
				return nil, errFake
			}

			return &aimodel.ChatResponse{ID: "recovered"}, nil
		},
		stream: func(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
			return nil, nil
		},
	}

	cb := NewCircuitBreakerMiddleware(
		WithFailureThreshold(threshold),
		WithResetTimeout(resetTimeout),
		withCircuitNowFn(nowFn),
	)
	wrapped := cb.Wrap(custom)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// Open the circuit.
	for i := range threshold {
		_, err := wrapped.ChatCompletion(ctx, req)
		if !errors.Is(err, errFake) {
			t.Fatalf("opening call %d: expected errFake, got %v", i, err)
		}
	}

	// Still open — rejected before timeout.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen before timeout, got %v", err)
	}

	// Advance past the reset timeout and switch the backend to healthy.
	advance(resetTimeout + time.Millisecond)
	failing = false

	// Probe request should succeed and close the circuit.
	resp, err := wrapped.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("probe request failed: %v", err)
	}

	if resp.ID != "recovered" {
		t.Fatalf("expected ID 'recovered', got %q", resp.ID)
	}

	// Subsequent requests must continue to succeed (circuit is closed again).
	_, err = wrapped.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("post-recovery request failed: %v", err)
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	const threshold = 2

	nowFn, advance := frozenClock(time.Now())
	resetTimeout := 10 * time.Second

	mock := &mockCompleter{chatErr: errFake}
	cb := NewCircuitBreakerMiddleware(
		WithFailureThreshold(threshold),
		WithResetTimeout(resetTimeout),
		withCircuitNowFn(nowFn),
	)
	wrapped := cb.Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// Open the circuit.
	for i := range threshold {
		_, err := wrapped.ChatCompletion(ctx, req)
		if !errors.Is(err, errFake) {
			t.Fatalf("opening call %d: expected errFake, got %v", i, err)
		}
	}

	// Advance past reset timeout — transitions to HalfOpen.
	advance(resetTimeout + time.Millisecond)

	// Probe fails — circuit should reopen immediately.
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, errFake) {
		t.Fatalf("probe: expected errFake, got %v", err)
	}

	// Next call must be rejected without reaching the backend.
	callsBefore := mock.chatCalls
	_, err = wrapped.ChatCompletion(ctx, req)

	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after failed probe, got %v", err)
	}

	if mock.chatCalls != callsBefore {
		t.Fatalf("backend was called after circuit re-opened")
	}
}

func TestCircuitBreaker_HalfOpenSingleProbe(t *testing.T) {
	const threshold = 2

	nowFn, advance := frozenClock(time.Now())
	resetTimeout := 10 * time.Second

	mock := &mockCompleter{chatErr: errFake}
	cb := NewCircuitBreakerMiddleware(
		WithFailureThreshold(threshold),
		WithResetTimeout(resetTimeout),
		withCircuitNowFn(nowFn),
	)
	wrapped := cb.Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// Open the circuit.
	for range threshold {
		_, _ = wrapped.ChatCompletion(ctx, req)
	}

	// Advance past reset timeout → HalfOpen.
	advance(resetTimeout + time.Millisecond)

	// First call in HalfOpen is the probe — allowed through.
	callsBefore := mock.chatCalls
	_, _ = wrapped.ChatCompletion(ctx, req)

	if mock.chatCalls != callsBefore+1 {
		t.Fatal("probe request should reach the backend")
	}

	// Second concurrent call while probe is in flight must be rejected.
	// (The probe above failed, so recordResult already re-opened the circuit.
	// But even if we simulate HalfOpen without the probe completing, the
	// probeInFlight flag blocks additional requests.)
	_, err := wrapped.ChatCompletion(ctx, req)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen for second request in HalfOpen, got %v", err)
	}
}

func TestCircuitBreaker_StreamSupport(t *testing.T) {
	const threshold = 2

	nowFn, advance := frozenClock(time.Now())
	resetTimeout := 10 * time.Second

	mock := &mockCompleter{streamErr: errFake}
	cb := NewCircuitBreakerMiddleware(
		WithFailureThreshold(threshold),
		WithResetTimeout(resetTimeout),
		withCircuitNowFn(nowFn),
	)
	wrapped := cb.Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{}

	// Open the circuit via stream calls.
	for i := range threshold {
		_, err := wrapped.ChatCompletionStream(ctx, req)
		if !errors.Is(err, errFake) {
			t.Fatalf("opening stream call %d: expected errFake, got %v", i, err)
		}
	}

	// Stream call must be rejected by the open circuit.
	_, err := wrapped.ChatCompletionStream(ctx, req)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen for stream, got %v", err)
	}

	if mock.streamCalls != threshold {
		t.Fatalf("expected %d backend stream calls, got %d", threshold, mock.streamCalls)
	}

	// Advance past reset timeout; switch backend to success.
	advance(resetTimeout + time.Millisecond)
	mock.streamErr = nil
	mock.streamResp = nil // nil Stream is fine for this test

	// Probe via stream should succeed and close the circuit.
	_, err = wrapped.ChatCompletionStream(ctx, req)
	if err != nil {
		t.Fatalf("stream probe failed: %v", err)
	}

	// Circuit is now closed — another stream call should reach the backend.
	callsBefore := mock.streamCalls
	_, err = wrapped.ChatCompletionStream(ctx, req)
	if err != nil {
		t.Fatalf("post-recovery stream call failed: %v", err)
	}

	if mock.streamCalls != callsBefore+1 {
		t.Fatalf("expected backend to be called after recovery, streamCalls=%d", mock.streamCalls)
	}
}
