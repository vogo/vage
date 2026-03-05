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

	"github.com/vogo/aimodel"
)

// Model wraps a ChatCompleter with a middleware chain.
type Model struct {
	completer aimodel.ChatCompleter
}

// ModelOption configures a Model.
type ModelOption func(*modelConfig)

type modelConfig struct {
	middlewares []Middleware
}

// WithMiddleware appends middlewares to the Model's chain.
func WithMiddleware(mws ...Middleware) ModelOption {
	return func(c *modelConfig) {
		c.middlewares = append(c.middlewares, mws...)
	}
}

// New creates a Model by chaining middlewares around base.
func New(base aimodel.ChatCompleter, opts ...ModelOption) *Model {
	cfg := &modelConfig{}
	for _, o := range opts {
		o(cfg)
	}

	completer := Chain(base, cfg.middlewares...)

	return &Model{completer: completer}
}

// ChatCompletion delegates to the wrapped completer.
func (m *Model) ChatCompletion(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	return m.completer.ChatCompletion(ctx, req)
}

// ChatCompletionStream delegates to the wrapped completer.
func (m *Model) ChatCompletionStream(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return m.completer.ChatCompletionStream(ctx, req)
}
