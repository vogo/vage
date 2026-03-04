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

package hook

import (
	"context"

	"github.com/vogo/vagent/schema"
)

// Hook is a synchronous event observer. OnEvent is called inline during
// dispatch and may be called concurrently from multiple goroutines;
// implementations must be safe for concurrent use and should be fast
// and non-blocking.
// Filter returns the event types this hook cares about. An empty slice
// means all events are delivered.
type Hook interface {
	OnEvent(ctx context.Context, event schema.Event) error
	Filter() []string
}

// AsyncHook is an asynchronous event observer that receives events via a
// channel. Start and Stop manage the consumer goroutine lifecycle.
// Filter returns the event types this hook cares about. An empty slice
// means all events are delivered.
type AsyncHook interface {
	EventChan() chan<- schema.Event
	Filter() []string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// HookFunc adapts a plain function into a Hook.
type HookFunc struct {
	fn    func(context.Context, schema.Event) error
	types []string
}

// NewHookFunc creates a HookFunc with the given handler and optional event type filter.
func NewHookFunc(fn func(context.Context, schema.Event) error, types ...string) HookFunc {
	return HookFunc{fn: fn, types: types}
}

// OnEvent delegates to the wrapped function.
func (h HookFunc) OnEvent(ctx context.Context, event schema.Event) error {
	return h.fn(ctx, event)
}

// Filter returns the configured event type filter.
func (h HookFunc) Filter() []string {
	return h.types
}
