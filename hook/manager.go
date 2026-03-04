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
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/vogo/vagent/schema"
)

// Manager dispatches events to registered sync and async hooks.
// It is safe for concurrent use.
type Manager struct {
	mu         sync.RWMutex
	syncHooks  []Hook
	asyncHooks []AsyncHook
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{}
}

// Register adds one or more synchronous hooks.
func (m *Manager) Register(hooks ...Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.syncHooks = append(m.syncHooks, hooks...)
}

// RegisterAsync adds one or more asynchronous hooks.
func (m *Manager) RegisterAsync(hooks ...AsyncHook) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.asyncHooks = append(m.asyncHooks, hooks...)
}

// Dispatch sends an event to all matching hooks.
// Sync hooks are called sequentially; errors are logged but do not interrupt dispatch.
// Async hooks receive events via non-blocking channel send; full channels cause the
// event to be dropped with a warning log.
// Dispatch returns early if the context is cancelled. It is safe to call on a nil Manager.
func (m *Manager) Dispatch(ctx context.Context, event schema.Event) {
	if m == nil {
		return
	}

	m.mu.RLock()
	syncHooks := m.syncHooks
	asyncHooks := m.asyncHooks
	m.mu.RUnlock()

	for _, h := range syncHooks {
		if ctx.Err() != nil {
			return
		}

		if !matches(h.Filter(), event.Type) {
			continue
		}

		if err := h.OnEvent(ctx, event); err != nil {
			slog.Warn("sync hook error",
				"event_type", event.Type,
				"error", err,
			)
		}
	}

	for _, h := range asyncHooks {
		if ctx.Err() != nil {
			return
		}

		if !matches(h.Filter(), event.Type) {
			continue
		}

		select {
		case h.EventChan() <- event:
		default:
			slog.Warn("async hook channel full, event dropped",
				"event_type", event.Type,
			)
		}
	}
}

// Start starts all registered async hooks. If a hook fails to start,
// all previously started hooks are stopped to prevent goroutine leaks.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i, h := range m.asyncHooks {
		if err := h.Start(ctx); err != nil {
			// Rollback: stop hooks [0, i) in reverse order.
			for j := i - 1; j >= 0; j-- {
				_ = m.asyncHooks[j].Stop(ctx)
			}

			return err
		}
	}

	return nil
}

// Stop stops all registered async hooks. It attempts to stop every hook
// and returns a combined error if any fail.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var errs []error

	for _, h := range m.asyncHooks {
		if err := h.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// matches returns true if the event type is accepted by the filter.
// An empty filter accepts all event types.
func matches(filter []string, eventType string) bool {
	if len(filter) == 0 {
		return true
	}

	return slices.Contains(filter, eventType)
}
