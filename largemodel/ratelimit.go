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

// ErrRateLimited is returned when a request exceeds the configured rate limit.
var ErrRateLimited = errors.New("largemodel: rate limit exceeded")

// RateLimitMiddleware enforces request and token rate limits using a sliding window.
type RateLimitMiddleware struct {
	mu             sync.Mutex
	requestsPerMin int
	tokensPerMin   int
	requestLog     []time.Time
	tokenLog       []tokenEntry
	nowFn          func() time.Time
}

type tokenEntry struct {
	at     time.Time
	tokens int
}

// RateLimitOption configures RateLimitMiddleware.
type RateLimitOption func(*RateLimitMiddleware)

// WithRequestsPerMin sets the maximum requests per minute.
func WithRequestsPerMin(n int) RateLimitOption {
	return func(m *RateLimitMiddleware) { m.requestsPerMin = n }
}

// WithTokensPerMin sets the maximum tokens per minute.
func WithTokensPerMin(n int) RateLimitOption {
	return func(m *RateLimitMiddleware) { m.tokensPerMin = n }
}

// withNowFn replaces the time function (for testing).
func withNowFn(fn func() time.Time) RateLimitOption {
	return func(m *RateLimitMiddleware) { m.nowFn = fn }
}

// NewRateLimitMiddleware creates a RateLimitMiddleware with the given options.
func NewRateLimitMiddleware(opts ...RateLimitOption) *RateLimitMiddleware {
	m := &RateLimitMiddleware{nowFn: time.Now}
	for _, o := range opts {
		o(m)
	}

	return m
}

// Wrap implements Middleware.
func (m *RateLimitMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			if err := m.allowRequest(); err != nil {
				return nil, err
			}

			resp, err := next.ChatCompletion(ctx, req)
			if err != nil {
				return nil, err
			}

			m.recordTokens(resp.Usage.TotalTokens)

			return resp, nil
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			if err := m.allowRequest(); err != nil {
				return nil, err
			}

			return next.ChatCompletionStream(ctx, req)
		},
	}
}

// allowRequest checks whether the current request is within rate limits.
func (m *RateLimitMiddleware) allowRequest() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.nowFn()
	windowStart := now.Add(-time.Minute)

	if m.requestsPerMin > 0 {
		m.requestLog = pruneTimestamps(m.requestLog, windowStart)
		if len(m.requestLog) >= m.requestsPerMin {
			return ErrRateLimited
		}

		m.requestLog = append(m.requestLog, now)
	}

	if m.tokensPerMin > 0 {
		m.tokenLog = pruneTokenEntries(m.tokenLog, windowStart)

		total := 0
		for _, e := range m.tokenLog {
			total += e.tokens
		}

		if total >= m.tokensPerMin {
			return ErrRateLimited
		}
	}

	return nil
}

// recordTokens records token usage for the sliding window.
func (m *RateLimitMiddleware) recordTokens(tokens int) {
	if m.tokensPerMin <= 0 || tokens <= 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tokenLog = append(m.tokenLog, tokenEntry{at: m.nowFn(), tokens: tokens})
}

// pruneTimestamps removes entries older than the window start.
func pruneTimestamps(entries []time.Time, windowStart time.Time) []time.Time {
	i := 0
	for i < len(entries) && entries[i].Before(windowStart) {
		i++
	}

	return entries[i:]
}

// pruneTokenEntries removes entries older than the window start.
func pruneTokenEntries(entries []tokenEntry, windowStart time.Time) []tokenEntry {
	i := 0
	for i < len(entries) && entries[i].at.Before(windowStart) {
		i++
	}

	return entries[i:]
}
