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

package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/vogo/vage/largemodel"

	"github.com/vogo/vage/schema"
)

// DebugSink receives debug events from the DebugMiddleware. Implementations
// must be safe for concurrent use. The fields map carries opaque key/value
// pairs describing the event; the middleware does not depend on any specific
// schema beyond what it itself emits.
type DebugSink interface {
	Emit(ctx context.Context, kind, correlationID string, fields map[string]any)
	NewCorrelationID() string
}

// NoopSink discards all events. Useful in tests and as a safe zero value.
type NoopSink struct{}

// Emit implements DebugSink.
func (NoopSink) Emit(_ context.Context, _, _ string, _ map[string]any) {}

// NewCorrelationID implements DebugSink.
func (NoopSink) NewCorrelationID() string { return "" }

// Debug record kinds emitted by DebugMiddleware.
const (
	KindLLMRequest  = "llm.request"
	KindLLMResponse = "llm.response"
	KindLLMError    = "llm.error"
)

// DebugMiddleware captures full LLM request and response data for both
// streaming and non-streaming calls and forwards them to a DebugSink.
type DebugMiddleware struct {
	sink DebugSink
}

// NewDebugMiddleware creates a DebugMiddleware. If sink is nil, a NoopSink is used.
func NewDebugMiddleware(sink DebugSink) *DebugMiddleware {
	if sink == nil {
		sink = NoopSink{}
	}

	return &DebugMiddleware{sink: sink}
}

// Wrap implements Middleware.
func (m *DebugMiddleware) Wrap(next largemodel.Caller) largemodel.Caller {
	return largemodel.BindCaller(next, m.call(next), m.callStream(next))
}

func (m *DebugMiddleware) call(next largemodel.Caller) func(ctx context.Context, req *largemodel.Request) (*largemodel.Response, error) {
	return func(ctx context.Context, req *largemodel.Request) (*largemodel.Response, error) {
		corr := m.sink.NewCorrelationID()
		start := time.Now()

		m.sink.Emit(ctx, KindLLMRequest, corr, requestFields(req, false))

		resp, err := next.Call(ctx, req)
		dur := time.Since(start)

		if err != nil {
			m.sink.Emit(ctx, KindLLMError, corr, map[string]any{
				"duration": dur,
				"error":    err.Error(),
			})

			return nil, err
		}

		m.sink.Emit(ctx, KindLLMResponse, corr, responseFields(resp, dur, false))

		return resp, nil
	}
}

func (m *DebugMiddleware) callStream(next largemodel.Caller) func(ctx context.Context, req *largemodel.Request) (*largemodel.Stream, error) {
	return func(ctx context.Context, req *largemodel.Request) (*largemodel.Stream, error) {
		corr := m.sink.NewCorrelationID()
		start := time.Now()

		m.sink.Emit(ctx, KindLLMRequest, corr, requestFields(req, true))

		s, err := next.CallStream(ctx, req)
		if err != nil {
			m.sink.Emit(ctx, KindLLMError, corr, map[string]any{
				"duration": time.Since(start),
				"error":    err.Error(),
			})

			return nil, err
		}

		// Accumulator state. onChunk runs from the consumer's Recv goroutine
		// while onDone may run from either Recv (on error/EOF) or Close (on
		// early termination). Stream guarantees only that Recv and Close are
		// safe to call concurrently, so we must guard the accumulator.
		var (
			mu        sync.Mutex
			acc       largemodel.StreamAccumulator
			lastUsage *schema.Usage
		)

		onChunk := func(chunk *largemodel.Chunk) {
			mu.Lock()
			defer mu.Unlock()

			if chunk.Usage != nil {
				lastUsage = chunk.Usage
			}

			acc.Add(chunk)
		}

		onDone := func(streamErr error) {
			mu.Lock()
			fields := map[string]any{
				"duration":      time.Since(start),
				"streamed":      true,
				"content":       acc.Text(),
				"tool_calls":    acc.ToolCalls(),
				"finish_reason": string(acc.FinishReason()),
				"usage":         lastUsage,
			}
			mu.Unlock()

			if streamErr != nil && !isEOF(streamErr) {
				fields["error"] = streamErr.Error()
			}

			m.sink.Emit(ctx, KindLLMResponse, corr, fields)
		}

		return largemodel.InterceptStream(s, onChunk, onDone), nil
	}
}

func requestFields(req *largemodel.Request, streamed bool) map[string]any {
	if req == nil {
		return map[string]any{}
	}

	return map[string]any{
		"model":               req.Model,
		"messages":            req.Messages,
		"tools":               req.Tools,
		"temperature":         req.Temperature,
		"max_tokens":          req.MaxTokens,
		"stream":              streamed,
		"prompt_caching":      req.PromptCaching,
		"stop":                req.Stop,
		"top_p":               req.TopP,
		"seed":                req.Seed,
		"frequency_penalty":   req.FrequencyPenalty,
		"presence_penalty":    req.PresencePenalty,
		"tool_choice":         req.ToolChoice,
		"provider_extensions": req.ProviderExtensions,
	}
}

func responseFields(resp *largemodel.Response, dur time.Duration, streamed bool) map[string]any {
	fields := map[string]any{
		"duration": dur,
		"streamed": streamed,
	}

	if resp == nil {
		return fields
	}

	fields["model"] = resp.Model
	fields["usage"] = resp.Usage
	fields["content"] = resp.Message.Text()
	fields["tool_calls"] = resp.Message.ToolCalls()
	fields["finish_reason"] = string(resp.FinishReason)

	return fields
}

func isEOF(err error) bool {
	return err != nil && errors.Is(err, io.EOF)
}

// NewCorrelationID generates a UUIDv4-like hex string for correlating events.
func NewCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return hex.EncodeToString(b[:])
}
