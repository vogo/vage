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

	"github.com/vogo/vage/schema"
)

// Model is a configured model endpoint: one protocol Caller with vage's
// governance middlewares wrapped around it. It is itself a Caller, so a Model
// can be nested or passed anywhere a plain Caller is accepted.
type Model struct {
	caller Caller
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
func New(base Caller, opts ...ModelOption) *Model {
	cfg := &modelConfig{}
	for _, o := range opts {
		o(cfg)
	}

	return &Model{caller: Chain(base, cfg.middlewares...)}
}

// Protocol reports the wire protocol of the underlying caller.
func (m *Model) Protocol() schema.Protocol {
	return m.caller.Protocol()
}

// Call performs one non-streaming model call through the middleware chain.
func (m *Model) Call(ctx context.Context, req *Request) (*Response, error) {
	return m.caller.Call(ctx, req)
}

// CallStream performs one streaming model call through the middleware chain.
func (m *Model) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return m.caller.CallStream(ctx, req)
}

var _ Caller = (*Model)(nil)
