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

// Package largemodel provides middleware for aimodel.ChatCompleter,
// adding retry, caching, rate limiting, timeout, and logging capabilities.
package largemodel

import (
	"context"

	"github.com/vogo/aimodel"
)

// Middleware wraps a ChatCompleter to add cross-cutting behavior.
type Middleware interface {
	Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter
}

// MiddlewareFunc adapts a plain function to the Middleware interface.
type MiddlewareFunc func(next aimodel.ChatCompleter) aimodel.ChatCompleter

// Wrap implements Middleware.
func (f MiddlewareFunc) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return f(next)
}

// Chain applies middlewares around base so that middlewares[0] is outermost
// and middlewares[len-1] is innermost (closest to base).
func Chain(base aimodel.ChatCompleter, middlewares ...Middleware) aimodel.ChatCompleter {
	wrapped := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		wrapped = middlewares[i].Wrap(wrapped)
	}

	return wrapped
}

// DefaultChain is like Chain but skips nil entries in the middleware slice.
// Recommended ordering: Log → CircuitBreaker → RateLimit → Retry → Timeout → Cache → base.
func DefaultChain(base aimodel.ChatCompleter, middlewares ...Middleware) aimodel.ChatCompleter {
	var mws []Middleware
	for _, mw := range middlewares {
		if mw != nil {
			mws = append(mws, mw)
		}
	}

	return Chain(base, mws...)
}

// completerFunc is a ChatCompleter implemented by two functions.
type completerFunc struct {
	chat   func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error)
	stream func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error)
}

func (c *completerFunc) ChatCompletion(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	return c.chat(ctx, req)
}

func (c *completerFunc) ChatCompletionStream(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return c.stream(ctx, req)
}
