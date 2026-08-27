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

package agent

import "slices"

// Middleware decorates one whole agent run. It is the seam for policies that
// must hold for a run as a unit — audit, tenancy, canned answers, response
// rewriting — and it is deliberately the only such seam: a run passes through
// each middleware exactly once, whether the caller entered through Run or
// through RunStream.
//
// Wrap receives the downstream RunFunc and returns the replacement. An
// implementation may:
//
//   - call next and return its response untouched (pure observation);
//   - call next and then rewrite or replace the response (post-processing);
//   - skip next entirely and return a synthesised response or an error
//     (short circuit — no model call and no tool execution happen).
//
// Three neighbouring extension points are NOT this one. hook.Hook observes
// lifecycle events and cannot change a result. StreamMiddleware intercepts
// events on their way to a stream consumer, not the run itself.
// largemodel.Middleware wraps a single model call, so it runs once per ReAct
// iteration and is where caching, rate limiting and timeouts belong.
//
// Concurrent runs of the same agent share one middleware instance, so
// implementations that keep state must be safe for concurrent use.
type Middleware interface {
	Wrap(next RunFunc) RunFunc
}

// MiddlewareFunc adapts a plain function to the Middleware interface.
type MiddlewareFunc func(next RunFunc) RunFunc

// Wrap implements Middleware.
func (f MiddlewareFunc) Wrap(next RunFunc) RunFunc { return f(next) }

// ChainMiddleware applies middlewares around base so that middlewares[0] is
// outermost and middlewares[len-1] is innermost (closest to base). Pre-next
// logic therefore runs in registration order and post-next rewriting runs in
// reverse. nil entries are skipped rather than panicking, so a conditionally
// built slice needs no compaction at the call site.
func ChainMiddleware(base RunFunc, middlewares ...Middleware) RunFunc {
	wrapped := base

	for _, mw := range slices.Backward(middlewares) {
		if mw == nil {
			continue
		}

		wrapped = mw.Wrap(wrapped)
	}

	return wrapped
}
