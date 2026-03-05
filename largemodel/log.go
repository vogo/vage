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
	"log/slog"
	"time"

	"github.com/vogo/aimodel"
)

// LogMiddleware logs chat completion requests and responses using slog.
type LogMiddleware struct {
	logger *slog.Logger
}

// LogOption configures LogMiddleware.
type LogOption func(*LogMiddleware)

// WithLogger sets a custom slog.Logger.
func WithLogger(l *slog.Logger) LogOption {
	return func(m *LogMiddleware) { m.logger = l }
}

// NewLogMiddleware creates a LogMiddleware with optional configuration.
func NewLogMiddleware(opts ...LogOption) *LogMiddleware {
	m := &LogMiddleware{logger: slog.Default()}
	for _, o := range opts {
		o(m)
	}

	return m
}

// Wrap implements Middleware.
func (m *LogMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			start := time.Now()
			m.logger.InfoContext(ctx, "chat_completion_start",
				"model", req.Model,
				"messages", len(req.Messages),
				"tools", len(req.Tools),
			)

			resp, err := next.ChatCompletion(ctx, req)
			duration := time.Since(start)

			if err != nil {
				m.logger.ErrorContext(ctx, "chat_completion_error",
					"model", req.Model,
					"duration_ms", duration.Milliseconds(),
					"error", err,
				)

				return nil, err
			}

			m.logger.InfoContext(ctx, "chat_completion_done",
				"model", req.Model,
				"duration_ms", duration.Milliseconds(),
				"prompt_tokens", resp.Usage.PromptTokens,
				"completion_tokens", resp.Usage.CompletionTokens,
				"total_tokens", resp.Usage.TotalTokens,
			)

			return resp, nil
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			m.logger.InfoContext(ctx, "chat_completion_stream_start",
				"model", req.Model,
				"messages", len(req.Messages),
			)

			s, err := next.ChatCompletionStream(ctx, req)
			if err != nil {
				m.logger.ErrorContext(ctx, "chat_completion_stream_error",
					"model", req.Model,
					"error", err,
				)

				return nil, err
			}

			return s, nil
		},
	}
}
