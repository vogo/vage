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
	"sync"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// stubWriter implements SessionHookWriter; counts AppendEvent / Create calls
// and lets tests inject failures.
type stubWriter struct {
	mu sync.Mutex

	appendCalls int
	createCalls int
	events      map[string][]schema.Event
	knownIDs    map[string]bool

	failAppendN int   // first N appends fail with appendErr
	appendErr   error // error returned while failAppendN > 0
	failCreate  error // err returned by Create
}

func newStubWriter() *stubWriter {
	return &stubWriter{
		events:   make(map[string][]schema.Event),
		knownIDs: make(map[string]bool),
	}
}

func (s *stubWriter) AppendEvent(_ context.Context, id string, e schema.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendCalls++

	if s.failAppendN > 0 {
		s.failAppendN--
		return s.appendErr
	}
	if !s.knownIDs[id] {
		return ErrSessionNotFound
	}
	s.events[id] = append(s.events[id], e)
	return nil
}

func (s *stubWriter) ListEvents(_ context.Context, id string) ([]schema.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownIDs[id] {
		return nil, ErrSessionNotFound
	}
	out := make([]schema.Event, len(s.events[id]))
	copy(out, s.events[id])
	return out, nil
}

func (s *stubWriter) Create(_ context.Context, sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	if s.failCreate != nil {
		return s.failCreate
	}
	if s.knownIDs[sess.ID] {
		return ErrSessionExists
	}
	s.knownIDs[sess.ID] = true
	return nil
}

func (s *stubWriter) addKnown(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownIDs[id] = true
}

func (s *stubWriter) snapshot() (appends, creates int, events map[string][]schema.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string][]schema.Event, len(s.events))
	for k, v := range s.events {
		cp[k] = append([]schema.Event{}, v...)
	}
	return s.appendCalls, s.createCalls, cp
}

// flushHook closes the channel and waits for the consumer to finish so we
// can read final counters without races.
func flushHook(t *testing.T, h *SessionHook) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func startHook(t *testing.T, store SessionHookWriter, opts ...Option) *SessionHook {
	t.Helper()
	h := NewSessionHook(store, opts...)
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return h
}

func TestHook_SkipsEmptySessionID(t *testing.T) {
	w := newStubWriter()
	h := startHook(t, w)
	h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "", schema.AgentStartData{})
	flushHook(t, h)

	appends, creates, _ := w.snapshot()
	if appends != 0 || creates != 0 {
		t.Fatalf("expected no calls; appends=%d creates=%d", appends, creates)
	}
}

func TestHook_ForwardsEventToKnownSession(t *testing.T) {
	w := newStubWriter()
	w.addKnown("s1")
	h := startHook(t, w)
	for range 3 {
		h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "s1", schema.AgentStartData{})
	}
	flushHook(t, h)

	appends, creates, ev := w.snapshot()
	if appends != 3 {
		t.Fatalf("expected 3 appends, got %d", appends)
	}
	if creates != 0 {
		t.Fatalf("expected 0 creates, got %d", creates)
	}
	if len(ev["s1"]) != 3 {
		t.Fatalf("expected 3 events stored, got %d", len(ev["s1"]))
	}
}

func TestHook_AutoCreateOnNotFound(t *testing.T) {
	w := newStubWriter()
	h := startHook(t, w) // autoCreate defaults to true

	h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "newbie", schema.AgentStartData{})
	flushHook(t, h)

	appends, creates, ev := w.snapshot()
	// First append fails (NotFound); Create succeeds; retry append succeeds.
	if creates != 1 {
		t.Fatalf("expected 1 create, got %d", creates)
	}
	if appends != 2 {
		t.Fatalf("expected 2 appends (1 failed + 1 retry), got %d", appends)
	}
	if len(ev["newbie"]) != 1 {
		t.Fatalf("expected 1 stored event, got %d", len(ev["newbie"]))
	}
}

func TestHook_AutoCreateDisabled(t *testing.T) {
	w := newStubWriter()
	h := startHook(t, w, WithAutoCreate(false))
	h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "missing", schema.AgentStartData{})
	flushHook(t, h)

	_, creates, _ := w.snapshot()
	if creates != 0 {
		t.Fatalf("expected no Create calls when autoCreate=false, got %d", creates)
	}
}

func TestHook_PersistentAppendErrorDoesNotBlock(t *testing.T) {
	w := newStubWriter()
	w.addKnown("err")
	w.failAppendN = 5
	w.appendErr = errors.New("disk full")

	h := startHook(t, w)
	for range 5 {
		h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "err", schema.AgentStartData{})
	}
	// Hook must not panic / block; we just expect Stop to complete in time.
	flushHook(t, h)
}

func TestHook_FilterPropagated(t *testing.T) {
	w := newStubWriter()
	h := NewSessionHook(w, WithFilter("agent_start", "agent_end"))
	got := h.Filter()
	if len(got) != 2 || got[0] != "agent_start" || got[1] != "agent_end" {
		t.Fatalf("filter mismatch: %v", got)
	}

	h2 := NewSessionHook(w, WithFilter())
	if got := h2.Filter(); got != nil {
		t.Fatalf("expected nil filter for empty WithFilter, got %v", got)
	}
}

func TestHook_StopIdempotent(t *testing.T) {
	w := newStubWriter()
	h := startHook(t, w)
	flushHook(t, h)
	// Second stop must not panic.
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

// panickingWriter panics on the first AppendEvent call, then behaves
// normally. Used to verify that a single misbehaving writer does not kill
// the consumer goroutine.
type panickingWriter struct {
	mu          sync.Mutex
	calls       int
	panicOnCall int
	stored      []schema.Event
}

func (p *panickingWriter) AppendEvent(_ context.Context, _ string, e schema.Event) error {
	p.mu.Lock()
	p.calls++
	cur := p.calls
	p.mu.Unlock()
	if cur == p.panicOnCall {
		panic("boom")
	}
	p.mu.Lock()
	p.stored = append(p.stored, e)
	p.mu.Unlock()
	return nil
}

func (p *panickingWriter) ListEvents(_ context.Context, _ string) ([]schema.Event, error) {
	return nil, nil
}

func (p *panickingWriter) Create(_ context.Context, _ *Session) error { return nil }

func (p *panickingWriter) snapshot() (calls, stored int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, len(p.stored)
}

func TestHook_RecoversFromPanicInWriter(t *testing.T) {
	w := &panickingWriter{panicOnCall: 1}
	h := startHook(t, w)
	for range 5 {
		h.EventChan() <- schema.NewEvent(schema.EventAgentStart, "agent", "s", schema.AgentStartData{})
	}
	flushHook(t, h)

	calls, stored := w.snapshot()
	// First call panics; the next 4 must be processed (consumer survived).
	if calls != 5 {
		t.Fatalf("expected 5 calls, got %d", calls)
	}
	if stored != 4 {
		t.Fatalf("expected 4 stored events after panic on call 1, got %d", stored)
	}
}

func TestHook_BufferSizeOption(t *testing.T) {
	w := newStubWriter()
	h := NewSessionHook(w, WithBufferSize(7))
	if cap(h.ch) != 7 {
		t.Fatalf("expected capacity 7, got %d", cap(h.ch))
	}
	h2 := NewSessionHook(w, WithBufferSize(0))
	if cap(h2.ch) != DefaultHookBufferSize {
		t.Fatalf("expected default %d, got %d", DefaultHookBufferSize, cap(h2.ch))
	}
}
