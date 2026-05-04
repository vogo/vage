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

// Package archivehook bridges the agent lifecycle to a vector.VectorStore
// by listening for EventAgentEnd and writing the agent's final message
// into the store as an embedded document.
//
// Design choice (per spec decision D): we use the existing async hook
// plumbing rather than the memory.Archiver path. Reasons:
//
//   - the framework already wires hook.Manager (trace + session); adding
//     a peer is trivial wiring;
//   - failures are fail-open by construction — a network blip during
//     embed/Add never blocks the agent loop;
//   - it does not collide with whatever memory.Archiver the caller has
//     configured (or hasn't), so adoption is opt-in and reversible.
//
// What this hook does NOT do (out of scope):
//
//   - tool dialog accumulation. The hook indexes only the final assistant
//     message. Subscribing to ToolResult / TextDelta and accumulating
//     per-session is a richer indexing strategy that callers can layer
//     on by writing their own AsyncHook.
//   - dedup. Each agent run produces one document. Re-runs (Resume) of
//     the same session create new documents with distinct IDs.
package archivehook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/vector"
)

// DefaultBufferSize matches session.DefaultHookBufferSize so the two
// async hooks can be configured side-by-side without surprises.
const DefaultBufferSize = 1024

// DefaultMinMessageBytes filters out trivially-short final messages
// (e.g. "ok", "done") that have no recall value. 16 bytes is a
// generous floor — short enough to keep a one-line tool result
// summary, long enough to drop pure acknowledgements.
const DefaultMinMessageBytes = 16

// Hook is the AsyncHook that performs auto-write on agent end.
//
// Concurrency: safe. EventChan is single-writer single-reader, mutated
// state is mutex-guarded.
type Hook struct {
	store    vector.VectorStore
	embedder vector.Embedder

	ch       chan schema.Event
	filter   []string
	minBytes int

	predicate func(sessionID string) bool

	wg       sync.WaitGroup
	stopOnce sync.Once

	warnMu      sync.Mutex
	lastWarnSID string
}

// Compile-time check.
var _ hook.AsyncHook = (*Hook)(nil)

// Option configures a Hook.
type Option func(*Hook)

// WithBufferSize sets the event channel capacity. Values <= 0 fall back
// to DefaultBufferSize.
func WithBufferSize(n int) Option {
	return func(h *Hook) {
		if n > 0 {
			h.ch = make(chan schema.Event, n)
		}
	}
}

// WithMinMessageBytes sets the minimum body length that triggers a
// write. Messages shorter than the threshold are skipped silently.
// Negative or zero values disable the filter.
func WithMinMessageBytes(n int) Option {
	return func(h *Hook) {
		h.minBytes = n
	}
}

// WithSessionPredicate restricts auto-write to the sessions for which
// the predicate returns true. nil (the default) writes for every
// session. Useful when the caller wants to gate auto-write behind a
// per-session flag (e.g. user opted out, internal scratch session).
func WithSessionPredicate(p func(sessionID string) bool) Option {
	return func(h *Hook) {
		h.predicate = p
	}
}

// New constructs a Hook bound to store + embedder. The hook is not
// active until registered with hook.Manager.RegisterAsync and the
// manager is started.
//
// Returns an error when store or embedder is nil — both are required;
// the caller should disable auto-write upstream rather than constructing
// a Hook that would silently drop every event.
func New(store vector.VectorStore, embedder vector.Embedder, opts ...Option) (*Hook, error) {
	if store == nil {
		return nil, errors.New("archivehook: store is required")
	}
	if embedder == nil {
		return nil, errors.New("archivehook: embedder is required")
	}
	h := &Hook{
		store:    store,
		embedder: embedder,
		ch:       make(chan schema.Event, DefaultBufferSize),
		filter:   []string{schema.EventAgentEnd},
		minBytes: DefaultMinMessageBytes,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// EventChan implements hook.AsyncHook.
func (h *Hook) EventChan() chan<- schema.Event { return h.ch }

// Filter implements hook.AsyncHook. We subscribe only to EventAgentEnd
// so the consumer goroutine never wakes up for unrelated events.
func (h *Hook) Filter() []string { return h.filter }

// Start spins up the consumer goroutine.
func (h *Hook) Start(_ context.Context) error {
	h.wg.Add(1)
	go h.consume()
	return nil
}

// Stop closes the channel and waits for the consumer to drain. Mirrors
// session.SessionHook so an integrator's lifecycle code can treat the
// two hooks identically.
func (h *Hook) Stop(ctx context.Context) error {
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

func (h *Hook) consume() {
	defer h.wg.Done()
	bg := context.Background()
	for ev := range h.ch {
		h.safeHandle(bg, ev)
	}
}

// safeHandle wraps handle with panic recovery — a panic from the
// embedder or store would otherwise leak the consumer goroutine and
// stall the EventChan.
func (h *Hook) safeHandle(ctx context.Context, ev schema.Event) {
	defer func() {
		if r := recover(); r != nil {
			h.warn("archivehook: panic during write", ev.SessionID, fmt.Errorf("%v", r))
		}
	}()
	h.handle(ctx, ev)
}

func (h *Hook) handle(ctx context.Context, ev schema.Event) {
	if ev.Type != schema.EventAgentEnd {
		return
	}
	if ev.SessionID == "" {
		return
	}
	if h.predicate != nil && !h.predicate(ev.SessionID) {
		return
	}
	data, ok := ev.Data.(schema.AgentEndData)
	if !ok {
		return
	}
	text := data.Message
	if h.minBytes > 0 && len(text) < h.minBytes {
		return
	}
	if text == "" {
		return
	}

	vec, err := h.embedder.Embed(ctx, text)
	if err != nil {
		h.warn("archivehook: embed failed", ev.SessionID, err)
		return
	}

	doc := vector.Document{
		ID:        buildDocID(ev),
		Text:      text,
		Embedding: vec,
		Metadata: map[string]any{
			"session_id":  ev.SessionID,
			"agent_id":    ev.AgentID,
			"stop_reason": string(data.StopReason),
		},
		CreatedAt: nonZeroTime(ev.Timestamp),
	}
	if err := h.store.Add(ctx, doc); err != nil {
		h.warn("archivehook: store add failed", ev.SessionID, err)
	}
}

// buildDocID returns a stable-ish document ID for the event. We
// concatenate session, agent, and timestamp so re-runs of the same
// session under the same agent yield distinct IDs, while a re-emitted
// event (e.g. duplicate dispatch) is idempotent at the qdrant level
// (the underlying UUIDv5 derivation makes Add an upsert).
func buildDocID(ev schema.Event) string {
	t := nonZeroTime(ev.Timestamp).UnixNano()
	return fmt.Sprintf("session=%s|agent=%s|ts=%d", ev.SessionID, ev.AgentID, t)
}

// warn dedups by session id so a misbehaving session does not flood
// logs. Mirrors session.SessionHook.warn.
func (h *Hook) warn(msg, sid string, err error) {
	h.warnMu.Lock()
	defer h.warnMu.Unlock()
	if sid != "" && sid == h.lastWarnSID {
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
