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

package schema

import "context"

type sessionIDCtxKey struct{}

type emitterCtxKey struct{}

// Emitter sends a single Event into the active stream. Returning an error
// normally means the stream is shutting down; most callers ignore it because
// the run is terminating anyway.
type Emitter func(Event) error

// WithSessionID attaches sessionID to ctx so tool handlers can read it via
// SessionIDFromContext. Empty sessionID is a no-op.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDCtxKey{}, sessionID)
}

// SessionIDFromContext returns the sessionID attached via WithSessionID, or
// empty string when absent.
func SessionIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDCtxKey{}).(string)
	return v
}

// WithEmitter attaches a stream emitter to ctx. Nil is a no-op.
func WithEmitter(ctx context.Context, e Emitter) context.Context {
	if e == nil {
		return ctx
	}
	return context.WithValue(ctx, emitterCtxKey{}, e)
}

// EmitterFromContext returns the Emitter attached via WithEmitter, or nil when
// absent.
func EmitterFromContext(ctx context.Context) Emitter {
	v, _ := ctx.Value(emitterCtxKey{}).(Emitter)
	return v
}
