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
	"sync"
	"time"

	"github.com/vogo/aimodel"
)

// ErrCircuitOpen is returned when the circuit breaker is in the open state.
var ErrCircuitOpen = errors.New("largemodel: circuit breaker is open")

type circuitState int

const (
	stateClosed circuitState = iota
	stateOpen
	stateHalfOpen
)

const (
	defaultFailureThreshold = 5
	defaultResetTimeout     = 30 * time.Second
)

// CircuitBreakerMiddleware implements a three-state circuit breaker pattern.
// In the Closed state all requests pass through normally. After failureThreshold
// consecutive failures the circuit moves to Open, where requests are rejected
// immediately with ErrCircuitOpen. Once resetTimeout elapses the circuit moves
// to HalfOpen to allow a single probe request. A successful probe closes the
// circuit; a failed probe re-opens it.
//
// Note: for streaming calls, only the stream creation error is observed.
// Mid-stream failures (e.g. server dropping the connection after the stream
// opens successfully) are not visible to the circuit breaker because
// *aimodel.Stream is a concrete type that cannot be wrapped.
type CircuitBreakerMiddleware struct {
	mu               sync.Mutex
	state            circuitState
	failureCount     int
	failureThreshold int
	resetTimeout     time.Duration
	lastFailureAt    time.Time
	probeInFlight    bool
	nowFn            func() time.Time
}

// CircuitBreakerOption configures CircuitBreakerMiddleware.
type CircuitBreakerOption func(*CircuitBreakerMiddleware)

// WithFailureThreshold sets the number of consecutive failures required to
// open the circuit. Defaults to 5.
func WithFailureThreshold(n int) CircuitBreakerOption {
	return func(m *CircuitBreakerMiddleware) { m.failureThreshold = n }
}

// WithResetTimeout sets how long to wait in the Open state before transitioning
// to HalfOpen to probe for recovery. Defaults to 30s.
func WithResetTimeout(d time.Duration) CircuitBreakerOption {
	return func(m *CircuitBreakerMiddleware) { m.resetTimeout = d }
}

// withCircuitNowFn replaces the time source used for timeout checks (for testing).
func withCircuitNowFn(fn func() time.Time) CircuitBreakerOption {
	return func(m *CircuitBreakerMiddleware) { m.nowFn = fn }
}

// NewCircuitBreakerMiddleware creates a CircuitBreakerMiddleware with optional
// configuration. The circuit starts in the Closed state.
func NewCircuitBreakerMiddleware(opts ...CircuitBreakerOption) *CircuitBreakerMiddleware {
	m := &CircuitBreakerMiddleware{
		failureThreshold: defaultFailureThreshold,
		resetTimeout:     defaultResetTimeout,
		nowFn:            time.Now,
	}
	for _, o := range opts {
		o(m)
	}

	return m
}

// allow checks whether a request should be permitted based on the current state.
// It returns ErrCircuitOpen when the circuit is open and the reset timeout has
// not yet elapsed.
func (m *CircuitBreakerMiddleware) allow() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch m.state {
	case stateClosed:
		return nil
	case stateHalfOpen:
		if m.probeInFlight {
			return ErrCircuitOpen
		}

		m.probeInFlight = true

		return nil
	case stateOpen:
		if m.nowFn().Sub(m.lastFailureAt) >= m.resetTimeout {
			m.state = stateHalfOpen
			m.probeInFlight = true

			return nil
		}

		return ErrCircuitOpen
	default:
		return nil
	}
}

// recordResult updates circuit state based on whether the last request succeeded
// or failed.
func (m *CircuitBreakerMiddleware) recordResult(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err == nil {
		// Any success resets the circuit to Closed.
		m.state = stateClosed
		m.failureCount = 0
		m.probeInFlight = false

		return
	}

	switch m.state {
	case stateClosed:
		m.failureCount++
		if m.failureCount >= m.failureThreshold {
			m.state = stateOpen
			m.lastFailureAt = m.nowFn()
		}
	case stateHalfOpen:
		// Probe failed — reopen the circuit immediately.
		m.state = stateOpen
		m.lastFailureAt = m.nowFn()
		m.probeInFlight = false
	}
}

// Wrap implements Middleware. It gates both ChatCompletion and
// ChatCompletionStream calls through the circuit breaker.
func (m *CircuitBreakerMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			if err := m.allow(); err != nil {
				return nil, err
			}

			resp, err := next.ChatCompletion(ctx, req)
			m.recordResult(err)

			return resp, err
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			if err := m.allow(); err != nil {
				return nil, err
			}

			s, err := next.ChatCompletionStream(ctx, req)
			m.recordResult(err)

			return s, err
		},
	}
}
