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
	"math/rand/v2"
	"net"
	"time"

	"github.com/vogo/aimodel"
)

const (
	defaultMaxRetries = 3
	defaultBaseDelay  = 1 * time.Second
	defaultMaxDelay   = 30 * time.Second
	jitterFraction    = 0.25
)

// retryableStatusCodes are HTTP status codes that indicate a transient error.
var retryableStatusCodes = map[int]bool{
	429: true, // Too Many Requests
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
}

// BackoffStrategy computes the delay before a retry attempt.
type BackoffStrategy interface {
	Delay(attempt int) time.Duration
}

// ExponentialBackoff implements BackoffStrategy with exponential delay and jitter.
type ExponentialBackoff struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Jitter    float64
}

// Delay returns the delay for the given attempt.
func (b *ExponentialBackoff) Delay(attempt int) time.Duration {
	delay := b.BaseDelay
	for range attempt {
		delay *= 2
		if delay > b.MaxDelay {
			delay = b.MaxDelay

			break
		}
	}

	jitter := time.Duration(float64(delay) * b.Jitter * rand.Float64())

	return delay + jitter
}

// RetryMiddleware retries failed ChatCompletion and ChatCompletionStream calls
// with exponential backoff.
type RetryMiddleware struct {
	maxRetries      int
	baseDelay       time.Duration
	maxDelay        time.Duration
	sleepFn         func(context.Context, time.Duration) error
	backoffStrategy BackoffStrategy
}

// RetryOption configures RetryMiddleware.
type RetryOption func(*RetryMiddleware)

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) RetryOption {
	return func(m *RetryMiddleware) { m.maxRetries = n }
}

// WithBaseDelay sets the initial backoff delay.
func WithBaseDelay(d time.Duration) RetryOption {
	return func(m *RetryMiddleware) { m.baseDelay = d }
}

// WithMaxDelay sets the maximum backoff delay.
func WithMaxDelay(d time.Duration) RetryOption {
	return func(m *RetryMiddleware) { m.maxDelay = d }
}

// WithBackoff sets a custom backoff strategy.
func WithBackoff(b BackoffStrategy) RetryOption {
	return func(m *RetryMiddleware) { m.backoffStrategy = b }
}

// withSleepFn replaces the sleep function (for testing).
func withSleepFn(fn func(context.Context, time.Duration) error) RetryOption {
	return func(m *RetryMiddleware) { m.sleepFn = fn }
}

// NewRetryMiddleware creates a RetryMiddleware with optional configuration.
func NewRetryMiddleware(opts ...RetryOption) *RetryMiddleware {
	m := &RetryMiddleware{
		maxRetries: defaultMaxRetries,
		baseDelay:  defaultBaseDelay,
		maxDelay:   defaultMaxDelay,
		sleepFn:    defaultSleep,
	}
	for _, o := range opts {
		o(m)
	}

	if m.backoffStrategy == nil {
		m.backoffStrategy = &ExponentialBackoff{
			BaseDelay: m.baseDelay,
			MaxDelay:  m.maxDelay,
			Jitter:    jitterFraction,
		}
	}

	return m
}

// Wrap implements Middleware.
func (m *RetryMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			var lastErr error
			for attempt := range m.maxRetries + 1 {
				resp, err := next.ChatCompletion(ctx, req)
				if err == nil {
					return resp, nil
				}

				lastErr = err

				if attempt >= m.maxRetries || !isRetryable(err) {
					return nil, err
				}

				delay := m.backoffStrategy.Delay(attempt)
				if err := m.sleepFn(ctx, delay); err != nil {
					return nil, lastErr
				}
			}

			return nil, lastErr
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			var lastErr error
			for attempt := range m.maxRetries + 1 {
				s, err := next.ChatCompletionStream(ctx, req)
				if err == nil {
					return s, nil
				}

				lastErr = err

				if attempt >= m.maxRetries || !isRetryable(err) {
					return nil, err
				}

				delay := m.backoffStrategy.Delay(attempt)
				if err := m.sleepFn(ctx, delay); err != nil {
					return nil, lastErr
				}
			}

			return nil, lastErr
		},
	}
}

// isRetryable checks whether an error should trigger a retry.
// It recognises API errors with retryable status codes, network timeouts,
// temporary network conditions, and unexpected EOF signals.
func isRetryable(err error) bool {
	var apiErr *aimodel.APIError
	if errors.As(err, &apiErr) {
		return retryableStatusCodes[apiErr.StatusCode]
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}

// defaultSleep sleeps for the given duration, respecting context cancellation.
func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
