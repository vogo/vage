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

// Package largemodel calls vendor model APIs and wraps them in the
// cross-cutting governance vage applies to every call: caching, rate limiting,
// timeouts, budgets, logging and metrics.
//
// vage speaks each vendor's native protocol directly — OpenAI Chat
// Completions, OpenAI Responses, and Anthropic Messages — rather than through
// a vendor-neutral abstraction. A model is bound to one protocol at
// configuration time, and the Caller for that protocol owns the translation
// between vage's Request/Response envelopes and that vendor's wire types.
// Middlewares wrap a Caller and see only the envelopes.
//
// Retrying a failed call and taking a sick endpoint out of rotation are not
// among those concerns, and no middleware here provides them. A Caller reaches
// its vendor through an aimodel pool — one endpoint or several, the shape is
// the same — and that pool owns the retries, the endpoint health and the
// failover. vage does not run a second attempt loop above it; see
// OpenAIChatComposeCaller for what the pool does and how to tune it.
package largemodel

// Middleware wraps a Caller to add cross-cutting behavior.
type Middleware interface {
	Wrap(next Caller) Caller
}

// MiddlewareFunc adapts a plain function to the Middleware interface.
type MiddlewareFunc func(next Caller) Caller

// Wrap implements Middleware.
func (f MiddlewareFunc) Wrap(next Caller) Caller {
	return f(next)
}

// Chain applies middlewares around base so that middlewares[0] is outermost
// and middlewares[len-1] is innermost (closest to base).
func Chain(base Caller, middlewares ...Middleware) Caller {
	wrapped := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		wrapped = middlewares[i].Wrap(wrapped)
	}

	return wrapped
}

// DefaultChain is like Chain but skips nil entries in the middleware slice.
// Recommended ordering: Log → RateLimit → Timeout → Cache → base.
func DefaultChain(base Caller, middlewares ...Middleware) Caller {
	var mws []Middleware

	for _, mw := range middlewares {
		if mw != nil {
			mws = append(mws, mw)
		}
	}

	return Chain(base, mws...)
}
