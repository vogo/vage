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

import (
	"context"
	"sync"
)

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

// EmitCustomData publishes an application-defined event onto the stream bound
// to ctx: one EventCustom carrying CustomEventData{name, payload}, with the
// SessionID inherited from ctx. It exists so a long-running tool handler can
// report intermediate progress without EventData being opened up to external
// implementations — the top-level event type stays fixed at EventCustom and
// only name varies.
//
// It is best-effort and never fails the caller:
//
//   - No emitter bound to ctx (WithEmitter was never called, e.g. under a
//     non-streaming executor): the call is a silent no-op, so the same tool
//     stays reusable outside TaskAgent.
//   - The emitter returns an error (stream closing, cancelled, rejected): the
//     event is dropped and the error is swallowed. Nothing propagates to the
//     tool handler and the tool result is unaffected.
//
// Because delivery is not confirmable, custom events are for observability
// only; they must not be the sole trigger of a business state transition, and
// nothing here promises persistence, replay, at-least-once delivery or
// cross-process compatibility.
//
// Name and payload conventions are documented on CustomEventData: a non-empty,
// stable, namespaced name and a JSON-serializable, credential-free payload are
// the caller's responsibility — neither is validated here. The payload is not
// copied, so mutating it afterwards can race with consumers.
//
// Calls from one tool are delivered through the same emitter in call order;
// events from tools running in parallel may interleave, so callers that need
// to correlate events back to a tool call should carry their own field in the
// payload.
func EmitCustomData(ctx context.Context, name string, payload any) {
	em := EmitterFromContext(ctx)
	if em == nil {
		return
	}

	_ = em(NewEvent(EventCustom, "", SessionIDFromContext(ctx), CustomEventData{
		Name:    name,
		Payload: payload,
	}))
}

type runValuesCtxKey struct{}

// WithRunValues binds a fresh, empty run-scoped value store to ctx and returns
// the derived context. Tool handlers reached through that context can then
// exchange temporary state via SetRunValue / GetRunValue.
//
// A store bound here always shadows one inherited from the parent context:
// that is what makes a new Run a new isolation boundary, so a nested agent
// starting its own run cannot read or clobber its parent's values.
//
// Agent implementations (TaskAgent does this on Run, RunStream and Resume) and
// custom executors are the intended callers. The store is process-local and
// lives exactly as long as the context is reachable — it is never persisted,
// checkpointed or keyed by SessionID.
func WithRunValues(ctx context.Context) context.Context {
	return context.WithValue(ctx, runValuesCtxKey{}, &sync.Map{})
}

// runValuesFromContext returns the store bound by WithRunValues, or nil when
// the context carries none. The store itself stays unexported so callers
// cannot retain it beyond the run.
func runValuesFromContext(ctx context.Context) *sync.Map {
	v, _ := ctx.Value(runValuesCtxKey{}).(*sync.Map)
	return v
}

// SetRunValue stores value under key in the run-scoped store bound to ctx,
// overwriting any previous value, and reports whether a store was present.
// Without WithRunValues the call is a no-op returning false, so tools stay
// reusable under executors that do not provide run values; callers that
// require the capability can check the result and fail loudly.
//
// Concurrent Set/Get calls from parallel tools are safe. Concurrent writes to
// the same key have no defined winner, and no compare-and-swap, transaction or
// tool ordering is promised — a mutable value stored here must be synchronized
// by its own owner.
func SetRunValue(ctx context.Context, key string, value any) bool {
	m := runValuesFromContext(ctx)
	if m == nil {
		return false
	}

	m.Store(key, value)

	return true
}

// GetRunValue returns the value stored under key by SetRunValue, together with
// whether it was found. A missing store and a missing key are reported the
// same way, as nil, false. The value is returned as stored, not copied; type
// assertions and stable, namespaced keys are the caller's responsibility.
func GetRunValue(ctx context.Context, key string) (any, bool) {
	m := runValuesFromContext(ctx)
	if m == nil {
		return nil, false
	}

	return m.Load(key)
}
