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
	"time"

	"github.com/vogo/vage/largemodel"

	"github.com/vogo/vage/schema"
)

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
func (m *MetricsMiddleware) Wrap(next largemodel.Caller) largemodel.Caller {
	return &largemodel.CallerFunc{
		Proto: next.Protocol(),
		Chat: func(ctx context.Context, req *largemodel.Request) (*largemodel.Response, error) {
			m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallStart, "", "", schema.LLMCallStartData{
				Model:    req.Model,
				Messages: len(req.Messages),
				Tools:    len(req.Tools),
			}))

			start := time.Now()

			resp, err := next.Call(ctx, req)
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
				CacheReadTokens:  resp.Usage.CacheReadTokens,
				CacheWriteTokens: resp.Usage.CacheWriteTokens,
				ReasoningTokens:  resp.Usage.ReasoningTokens,
			}))

			return resp, nil
		},
		ChatStream: func(ctx context.Context, req *largemodel.Request) (*largemodel.Stream, error) {
			m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallStart, "", "", schema.LLMCallStartData{
				Model:    req.Model,
				Messages: len(req.Messages),
				Tools:    len(req.Tools),
				Stream:   true,
			}))

			start := time.Now()

			s, err := next.CallStream(ctx, req)
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
			return largemodel.WrapStreamClose(s, func(usage *schema.Usage) {
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
					data.CacheReadTokens = usage.CacheReadTokens
					data.CacheWriteTokens = usage.CacheWriteTokens
					data.ReasoningTokens = usage.ReasoningTokens
				}

				m.dispatch(ctx, schema.NewEvent(schema.EventLLMCallEnd, "", "", data))
			}), nil
		},
	}
}
