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
)

// TimeoutMiddleware adds a per-call context deadline to ChatCompletion calls.
// Stream calls pass the caller's context through unchanged so that the timeout
// does not cancel an in-progress stream after the first chunk arrives.
type TimeoutMiddleware struct {
	timeout time.Duration
}

// NewTimeoutMiddleware creates a TimeoutMiddleware with the given duration.
func NewTimeoutMiddleware(d time.Duration) *TimeoutMiddleware {
	return &TimeoutMiddleware{timeout: d}
}

// Wrap implements Middleware.
func (m *TimeoutMiddleware) Wrap(next Caller) Caller {
	return &CallerFunc{
		Proto: next.Protocol(),
		Chat: func(ctx context.Context, req *Request) (*Response, error) {
			ctx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()

			return next.Call(ctx, req)
		},
		ChatStream: func(ctx context.Context, req *Request) (*Stream, error) {
			// Pass the caller's context directly. Applying a timeout here would
			// cancel the derived context via defer cancel() as soon as
			// ChatCompletionStream returns, killing the stream before the caller
			// has consumed any chunks.
			return next.CallStream(ctx, req)
		},
	}
}
