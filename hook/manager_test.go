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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vagent/schema"
)

// --- test helpers ---

type spyHook struct {
	filter []string
	events []schema.Event
	err    error
}

func (s *spyHook) OnEvent(_ context.Context, e schema.Event) error {
	s.events = append(s.events, e)
	return s.err
}

func (s *spyHook) Filter() []string { return s.filter }

type spyAsyncHook struct {
	ch      chan schema.Event
	filter  []string
	started bool
	stopped bool
	startFn func() error
	stopFn  func() error
}

func newSpyAsyncHook(bufSize int, filter []string) *spyAsyncHook {
	return &spyAsyncHook{
		ch:     make(chan schema.Event, bufSize),
		filter: filter,
	}
}

func (s *spyAsyncHook) EventChan() chan<- schema.Event { return s.ch }
func (s *spyAsyncHook) Filter() []string               { return s.filter }

func (s *spyAsyncHook) Start(_ context.Context) error {
	s.started = true
	if s.startFn != nil {
		return s.startFn()
	}

	return nil
}

func (s *spyAsyncHook) Stop(_ context.Context) error {
	s.stopped = true
	if s.stopFn != nil {
		return s.stopFn()
	}

	return nil
}

func makeEvent(eventType string) schema.Event {
	return schema.NewEvent(eventType, "agent", "sess", nil)
}

// --- Manager tests ---

func TestManager_DispatchSyncHook(t *testing.T) {
	m := NewManager()
	spy := &spyHook{}
	m.Register(spy)

	event := makeEvent(schema.EventTextDelta)
	m.Dispatch(context.Background(), event)

	if len(spy.events) != 1 {
		t.Fatalf("got %d events, want 1", len(spy.events))
	}

	if spy.events[0].Type != schema.EventTextDelta {
		t.Errorf("event type = %q, want %q", spy.events[0].Type, schema.EventTextDelta)
	}
}

func TestManager_DispatchSyncHookWithFilter(t *testing.T) {
	m := NewManager()
	spy := &spyHook{filter: []string{schema.EventError}}
	m.Register(spy)

	m.Dispatch(context.Background(), makeEvent(schema.EventTextDelta))
	m.Dispatch(context.Background(), makeEvent(schema.EventError))

	if len(spy.events) != 1 {
		t.Fatalf("got %d events, want 1", len(spy.events))
	}

	if spy.events[0].Type != schema.EventError {
		t.Errorf("event type = %q, want %q", spy.events[0].Type, schema.EventError)
	}
}

func TestManager_SyncHookErrorDoesNotBlock(t *testing.T) {
	m := NewManager()

	failing := &spyHook{err: errors.New("boom")}
	passing := &spyHook{}
	m.Register(failing, passing)

	m.Dispatch(context.Background(), makeEvent(schema.EventAgentStart))

	if len(failing.events) != 1 {
		t.Errorf("failing hook got %d events, want 1", len(failing.events))
	}

	if len(passing.events) != 1 {
		t.Errorf("passing hook got %d events, want 1", len(passing.events))
	}
}

func TestManager_DispatchAsyncHook(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(10, nil)
	m.RegisterAsync(spy)

	event := makeEvent(schema.EventToolResult)
	m.Dispatch(context.Background(), event)

	select {
	case got := <-spy.ch:
		if got.Type != schema.EventToolResult {
			t.Errorf("event type = %q, want %q", got.Type, schema.EventToolResult)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async event")
	}
}

func TestManager_DispatchAsyncHookWithFilter(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(10, []string{schema.EventAgentEnd})
	m.RegisterAsync(spy)

	m.Dispatch(context.Background(), makeEvent(schema.EventTextDelta))
	m.Dispatch(context.Background(), makeEvent(schema.EventAgentEnd))

	select {
	case got := <-spy.ch:
		if got.Type != schema.EventAgentEnd {
			t.Errorf("event type = %q, want %q", got.Type, schema.EventAgentEnd)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async event")
	}

	// Channel should be empty — the text_delta was filtered out.
	select {
	case e := <-spy.ch:
		t.Fatalf("unexpected event: %v", e)
	default:
	}
}

func TestManager_AsyncHookFullChannelDoesNotBlock(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(1, nil)
	m.RegisterAsync(spy)

	// Fill the channel.
	m.Dispatch(context.Background(), makeEvent(schema.EventAgentStart))
	// This should not block even though the channel is full.
	m.Dispatch(context.Background(), makeEvent(schema.EventAgentEnd))

	got := <-spy.ch
	if got.Type != schema.EventAgentStart {
		t.Errorf("event type = %q, want %q", got.Type, schema.EventAgentStart)
	}
}

func TestManager_StartStop(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(1, nil)
	m.RegisterAsync(spy)

	ctx := context.Background()

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if !spy.started {
		t.Error("hook not started")
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if !spy.stopped {
		t.Error("hook not stopped")
	}
}

func TestManager_StartError(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(1, nil)
	spy.startFn = func() error { return errors.New("start failed") }
	m.RegisterAsync(spy)

	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start() expected error")
	}
}

func TestManager_StopError(t *testing.T) {
	m := NewManager()
	spy := newSpyAsyncHook(1, nil)
	spy.stopFn = func() error { return errors.New("stop failed") }
	m.RegisterAsync(spy)

	if err := m.Stop(context.Background()); err == nil {
		t.Fatal("Stop() expected error")
	}
}

func TestManager_EmptyDispatch(t *testing.T) {
	m := NewManager()
	// Should not panic with no hooks registered.
	m.Dispatch(context.Background(), makeEvent(schema.EventTextDelta))
}

func TestManager_ConcurrentDispatch(t *testing.T) {
	m := NewManager()

	var count atomic.Int64
	h := NewHookFunc(func(_ context.Context, _ schema.Event) error {
		count.Add(1)
		return nil
	})
	m.Register(h)

	const goroutines = 50
	const eventsPerGoroutine = 20

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			for range eventsPerGoroutine {
				m.Dispatch(context.Background(), makeEvent(schema.EventTextDelta))
			}
		}()
	}

	wg.Wait()

	want := int64(goroutines * eventsPerGoroutine)
	if got := count.Load(); got != want {
		t.Errorf("dispatch count = %d, want %d", got, want)
	}
}

func TestManager_RegisterMultiple(t *testing.T) {
	m := NewManager()
	s1 := &spyHook{}
	s2 := &spyHook{}
	m.Register(s1, s2)

	m.Dispatch(context.Background(), makeEvent(schema.EventAgentStart))

	if len(s1.events) != 1 {
		t.Errorf("s1 got %d events, want 1", len(s1.events))
	}

	if len(s2.events) != 1 {
		t.Errorf("s2 got %d events, want 1", len(s2.events))
	}
}

func TestManager_StopCollectsAllErrors(t *testing.T) {
	m := NewManager()

	s1 := newSpyAsyncHook(1, nil)
	s1.stopFn = func() error { return errors.New("stop1 failed") }

	s2 := newSpyAsyncHook(1, nil)
	s2.stopFn = func() error { return errors.New("stop2 failed") }

	m.RegisterAsync(s1, s2)

	err := m.Stop(context.Background())
	if err == nil {
		t.Fatal("Stop() expected error")
	}

	if !s1.stopped || !s2.stopped {
		t.Error("Stop() should attempt to stop all hooks")
	}

	// Both error messages should be present.
	msg := err.Error()
	if !strings.Contains(msg, "stop1 failed") || !strings.Contains(msg, "stop2 failed") {
		t.Errorf("error = %q, want both stop1 and stop2 failures", msg)
	}
}

func TestManager_StartRollbackOnFailure(t *testing.T) {
	m := NewManager()

	s1 := newSpyAsyncHook(1, nil)
	s2 := newSpyAsyncHook(1, nil)
	s2.startFn = func() error { return errors.New("start2 failed") }

	m.RegisterAsync(s1, s2)

	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("Start() expected error")
	}

	if !s1.started {
		t.Error("s1 should have been started")
	}

	if !s1.stopped {
		t.Error("s1 should have been rolled back (stopped)")
	}

	// s2.started is true because Start() sets it before startFn runs,
	// but Start() returned an error so the manager should have rolled back s1.
	if !s2.started {
		t.Error("s2 Start() was called")
	}
}

func TestManager_DispatchRespectsContextCancellation(t *testing.T) {
	m := NewManager()

	var count atomic.Int64
	h := NewHookFunc(func(_ context.Context, _ schema.Event) error {
		count.Add(1)
		return nil
	})
	m.Register(h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	m.Dispatch(ctx, makeEvent(schema.EventTextDelta))

	if got := count.Load(); got != 0 {
		t.Errorf("dispatch count = %d after cancelled context, want 0", got)
	}
}

func TestManager_NilManagerDispatch(t *testing.T) {
	var m *Manager
	// Should not panic.
	m.Dispatch(context.Background(), makeEvent(schema.EventTextDelta))
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name      string
		filter    []string
		eventType string
		want      bool
	}{
		{"empty filter matches all", nil, schema.EventTextDelta, true},
		{"matching filter", []string{schema.EventError}, schema.EventError, true},
		{"non-matching filter", []string{schema.EventError}, schema.EventTextDelta, false},
		{"multi-filter match", []string{schema.EventError, schema.EventAgentEnd}, schema.EventAgentEnd, true},
		{"multi-filter no match", []string{schema.EventError, schema.EventAgentEnd}, schema.EventTextDelta, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matches(tt.filter, tt.eventType); got != tt.want {
				t.Errorf("matches(%v, %q) = %v, want %v", tt.filter, tt.eventType, got, tt.want)
			}
		})
	}
}
