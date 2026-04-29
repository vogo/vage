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

package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

// DefaultHookBufferSize matches the default used by vv/traces/tracelog so
// the two hooks can be configured side-by-side without surprises.
const DefaultHookBufferSize = 1024

// SessionHookWriter is the narrow subset of SessionStore that SessionHook
// actually needs. Depending on it (instead of the full SessionStore) keeps
// SessionHook cheap to mock and signals intent.
type SessionHookWriter interface {
	SessionEventStore
	Create(ctx context.Context, s *Session) error
}

// SessionHook is a vage hook.AsyncHook that writes every event carrying a
// non-empty SessionID to the configured SessionStore via AppendEvent.
//
// AutoCreate (default true) bridges the gap between vage's current "callers
// pick their own session ids" model and the new "SessionStore expects
// Create-then-Append" contract: when AppendEvent returns ErrSessionNotFound,
// SessionHook calls Create with a freshly-minted Session{ID, AgentID,
// State=Active, CreatedAt=event.Timestamp} and retries once. Set to false
// when integrators want to enforce explicit session lifecycle.
//
// Failure handling: errors never block the agent loop. Per-session-id warn
// dedup ensures a misbehaving id doesn't flood logs.
type SessionHook struct {
	store      SessionHookWriter
	ch         chan schema.Event
	filter     []string
	autoCreate bool

	wg       sync.WaitGroup
	stopOnce sync.Once

	warnMu      sync.Mutex
	lastWarnSID string
}

// Compile-time check.
var _ hook.AsyncHook = (*SessionHook)(nil)

// Option configures a SessionHook.
type Option func(*SessionHook)

// WithBufferSize sets the event channel capacity. Values <= 0 fall back to
// DefaultHookBufferSize.
func WithBufferSize(n int) Option {
	return func(h *SessionHook) {
		if n > 0 {
			h.ch = make(chan schema.Event, n)
		}
	}
}

// WithFilter restricts the event types that the hook subscribes to. An
// empty filter (the default) subscribes to all events.
func WithFilter(types ...string) Option {
	return func(h *SessionHook) {
		if len(types) == 0 {
			h.filter = nil
			return
		}
		h.filter = append([]string{}, types...)
	}
}

// WithAutoCreate toggles the automatic Session creation behaviour described
// on SessionHook. Default is true.
func WithAutoCreate(b bool) Option {
	return func(h *SessionHook) { h.autoCreate = b }
}

// NewSessionHook constructs a SessionHook bound to store. The hook is not
// active until registered with hook.Manager.RegisterAsync and the manager
// is started.
func NewSessionHook(store SessionHookWriter, opts ...Option) *SessionHook {
	h := &SessionHook{
		store:      store,
		ch:         make(chan schema.Event, DefaultHookBufferSize),
		autoCreate: true,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// EventChan implements hook.AsyncHook.
func (h *SessionHook) EventChan() chan<- schema.Event { return h.ch }

// Filter implements hook.AsyncHook.
func (h *SessionHook) Filter() []string { return h.filter }

// Start spins up the consumer goroutine.
func (h *SessionHook) Start(_ context.Context) error {
	h.wg.Add(1)
	go h.consume()
	return nil
}

// Stop closes the channel and waits for the consumer to drain.
func (h *SessionHook) Stop(ctx context.Context) error {
	var stopErr error
	h.stopOnce.Do(func() {
		close(h.ch)
		done := make(chan struct{})
		go func() {
			h.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			stopErr = ctx.Err()
		}
	})
	return stopErr
}

func (h *SessionHook) consume() {
	defer h.wg.Done()
	bg := context.Background()

	for ev := range h.ch {
		if ev.SessionID == "" {
			continue
		}
		h.safeHandle(bg, ev)
	}
}

// safeHandle wraps handle with panic recovery so that a single misbehaving
// SessionHookWriter call does not kill the consumer goroutine and leave the
// EventChan permanently blocked. A killed consumer is far more dangerous
// than a single dropped event because every subsequent send on EventChan
// would deadlock the agent loop.
func (h *SessionHook) safeHandle(ctx context.Context, ev schema.Event) {
	defer func() {
		if r := recover(); r != nil {
			h.warn("session hook: writer panicked", ev.SessionID, fmt.Errorf("panic: %v", r))
		}
	}()
	h.handle(ctx, ev)
}

func (h *SessionHook) handle(ctx context.Context, ev schema.Event) {
	err := h.store.AppendEvent(ctx, ev.SessionID, ev)
	if err == nil {
		return
	}

	if errors.Is(err, ErrSessionNotFound) && h.autoCreate {
		seed := &Session{
			ID:        ev.SessionID,
			AgentID:   ev.AgentID,
			State:     StateActive,
			CreatedAt: nonZeroTime(ev.Timestamp),
		}
		if cerr := h.store.Create(ctx, seed); cerr != nil && !errors.Is(cerr, ErrSessionExists) {
			h.warn("session hook: auto-create failed", ev.SessionID, cerr)
			return
		}
		if err = h.store.AppendEvent(ctx, ev.SessionID, ev); err == nil {
			return
		}
	}

	h.warn("session hook: append event failed", ev.SessionID, err)
}

func (h *SessionHook) warn(msg, sid string, err error) {
	h.warnMu.Lock()
	defer h.warnMu.Unlock()
	if sid == h.lastWarnSID {
		return
	}
	h.lastWarnSID = sid
	slog.Warn(msg, "session_id", sid, "err", err)
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
