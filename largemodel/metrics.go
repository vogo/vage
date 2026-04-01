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
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// DispatchFunc is the function signature for dispatching events.
// Compatible with hook.Manager.Dispatch.
type DispatchFunc func(ctx context.Context, event schema.Event)

// MetricsMiddleware dispatches LLM call lifecycle events to an event system.
// It accepts a DispatchFunc (e.g. hook.Manager.Dispatch) to decouple from the hook package.
type MetricsMiddleware struct {
	dispatch DispatchFunc
}

// NewMetricsMiddleware creates a MetricsMiddleware with the given dispatch function.
// Panics if dispatch is nil.
func NewMetricsMiddleware(dispatch DispatchFunc) *MetricsMiddleware {
	if dispatch == nil {
		panic("largemodel: NewMetricsMiddleware requires a non-nil dispatch function")
	}

	return &MetricsMiddleware{dispatch: dispatch}
}

// Wrap implements Middleware.
func (m *MetricsMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallStart, "", "", schema.LLMCallStartData{
				Model:    req.Model,
				Messages: len(req.Messages),
				Tools:    len(req.Tools),
			}))

			start := time.Now()

			resp, err := next.ChatCompletion(ctx, req)
			duration := time.Since(start).Milliseconds()

			if err != nil {
				m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallError, "", "", schema.LLMCallErrorData{
					Model:    req.Model,
					Duration: duration,
					Error:    err.Error(),
				}))

				return nil, err
			}

			m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallEnd, "", "", schema.LLMCallEndData{
				Model:            req.Model,
				Duration:         duration,
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
			}))

			return resp, nil
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallStart, "", "", schema.LLMCallStartData{
				Model:    req.Model,
				Messages: len(req.Messages),
				Tools:    len(req.Tools),
				Stream:   true,
			}))

			start := time.Now()

			s, err := next.ChatCompletionStream(ctx, req)
			if err != nil {
				duration := time.Since(start).Milliseconds()
				m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallError, "", "", schema.LLMCallErrorData{
					Model:    req.Model,
					Duration: duration,
					Error:    err.Error(),
					Stream:   true,
				}))

				return nil, err
			}

			// Return a wrapped stream that emits EventLLMCallEnd with usage on close.
			return aimodel.WrapStream(s, func(usage *aimodel.Usage) {
				duration := time.Since(start).Milliseconds()
				data := schema.LLMCallEndData{
					Model:    req.Model,
					Duration: duration,
					Stream:   true,
				}

				if usage != nil {
					data.PromptTokens = usage.PromptTokens
					data.CompletionTokens = usage.CompletionTokens
					data.TotalTokens = usage.TotalTokens
				}

				m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallEnd, "", "", data))
			}), nil
		},
	}
}
