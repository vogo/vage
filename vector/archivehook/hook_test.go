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

package archivehook

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/vector"
)

// drainEventually waits for the consumer goroutine to process the
// event(s) by polling a counter. 200ms is generous for an in-process
// HashEmbedder + MapVectorStore path while still being short enough
// that test wall-time stays low.
func drainEventually(t *testing.T, target *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if target.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d, last=%d", want, target.Load())
}

func newHookWithStore(t *testing.T, opts ...Option) (*Hook, *vector.MapVectorStore, *vector.HashEmbedder) {
	t.Helper()
	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(64)
	h, err := New(store, emb, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = h.Stop(context.Background())
	})
	return h, store, emb
}

// asAgentEndEvent constructs the canonical EventAgentEnd shape the
// production hook expects. The test must produce the same struct that
// real callers do.
func asAgentEndEvent(sessionID, agentID, message string) schema.Event {
	return schema.Event{
		Type:      schema.EventAgentEnd,
		AgentID:   agentID,
		SessionID: sessionID,
		Timestamp: time.Now(),
		Data: schema.AgentEndData{
			Message:    message,
			StopReason: schema.StopReasonComplete,
			Duration:   42,
		},
	}
}

func TestNew_RequiresStoreAndEmbedder(t *testing.T) {
	if _, err := New(nil, vector.NewHashEmbedder(8)); err == nil {
		t.Error("expected error when store is nil")
	}
	if _, err := New(vector.NewMapVectorStore(), nil); err == nil {
		t.Error("expected error when embedder is nil")
	}
}

func TestHandle_HappyPath_AddsDocument(t *testing.T) {
	h, store, _ := newHookWithStore(t)

	ev := asAgentEndEvent("sess-1", "coder", "Implemented the feature with tests passing")
	h.EventChan() <- ev

	var lenCounter atomic.Int32
	go func() {
		// Crude but sufficient: poll Len in the background.
		for range 200 {
			lenCounter.Store(int32(store.Len()))
			if store.Len() > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	drainEventually(t, &lenCounter, 1)

	if got := store.Len(); got != 1 {
		t.Fatalf("store.Len() = %d, want 1", got)
	}
	docs, _ := store.List(context.Background())
	doc := docs[0]
	if doc.Text != "Implemented the feature with tests passing" {
		t.Errorf("Text = %q", doc.Text)
	}
	if got, _ := doc.Metadata["session_id"].(string); got != "sess-1" {
		t.Errorf("session_id = %q", got)
	}
	if got, _ := doc.Metadata["agent_id"].(string); got != "coder" {
		t.Errorf("agent_id = %q", got)
	}
}

func TestHandle_SkipsWrongEventType(t *testing.T) {
	h, store, _ := newHookWithStore(t)
	// Force-bypass Filter() by sending directly through the channel —
	// production fan-out by Manager would have filtered, but the
	// consumer must still ignore non-AgentEnd events defensively.
	h.EventChan() <- schema.Event{
		Type:      schema.EventToolResult,
		SessionID: "sess-1",
		Data:      schema.ToolResultData{ToolName: "x"},
	}
	time.Sleep(50 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Errorf("expected nothing written, got Len=%d", got)
	}
}

func TestHandle_SkipsEmptyMessage(t *testing.T) {
	h, store, _ := newHookWithStore(t)
	ev := asAgentEndEvent("sess-1", "coder", "")
	h.EventChan() <- ev
	time.Sleep(50 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Errorf("expected skip on empty message, got Len=%d", got)
	}
}

func TestHandle_SkipsBelowMinBytes(t *testing.T) {
	h, store, _ := newHookWithStore(t, WithMinMessageBytes(32))
	ev := asAgentEndEvent("sess-1", "coder", "short reply")
	h.EventChan() <- ev
	time.Sleep(50 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Errorf("expected skip below threshold, got Len=%d", got)
	}
}

func TestHandle_SessionPredicate(t *testing.T) {
	h, store, _ := newHookWithStore(t, WithSessionPredicate(func(sid string) bool {
		return sid == "allowed"
	}))
	h.EventChan() <- asAgentEndEvent("denied", "coder", "long enough message goes here")
	h.EventChan() <- asAgentEndEvent("allowed", "coder", "long enough message goes here")
	time.Sleep(80 * time.Millisecond)
	if got := store.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 (predicate should filter)", got)
	}
	docs, _ := store.List(context.Background())
	if got, _ := docs[0].Metadata["session_id"].(string); got != "allowed" {
		t.Errorf("survived doc has session_id = %q, want allowed", got)
	}
}

func TestHandle_SkipsEmptySessionID(t *testing.T) {
	h, store, _ := newHookWithStore(t)
	ev := asAgentEndEvent("", "coder", "long enough message body here")
	h.EventChan() <- ev
	time.Sleep(50 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Errorf("expected skip on empty session_id, got Len=%d", got)
	}
}

func TestHandle_EmbedderFailure_FailOpen(t *testing.T) {
	store := vector.NewMapVectorStore()
	emb := vector.EmbedderFunc(func(_ context.Context, _ string) ([]float32, error) {
		return nil, errors.New("synthetic embed failure")
	})
	h, err := New(store, emb)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.Stop(context.Background()) }()

	h.EventChan() <- asAgentEndEvent("sess-1", "coder", "long enough message body here")
	time.Sleep(80 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Errorf("embed failure should not have written, got Len=%d", got)
	}
	// The hook must continue to accept further events afterwards.
	if h.EventChan() == nil {
		t.Error("EventChan went nil after embed error")
	}
}

func TestHandle_StoreFailure_FailOpen(t *testing.T) {
	store := &failingStore{addErr: errors.New("synthetic store failure")}
	emb := vector.NewHashEmbedder(8)
	h, err := New(store, emb)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.Stop(context.Background()) }()

	h.EventChan() <- asAgentEndEvent("sess-1", "coder", "long enough message body here")
	time.Sleep(80 * time.Millisecond)
	if got := store.addCalls.Load(); got != 1 {
		t.Errorf("expected 1 Add call, got %d", got)
	}
}

func TestStop_Idempotent(t *testing.T) {
	h, _, _ := newHookWithStore(t)
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 1: %v", err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 2 (must be idempotent): %v", err)
	}
}

func TestFilter_OnlyAgentEnd(t *testing.T) {
	h, _, _ := newHookWithStore(t)
	if got := h.Filter(); len(got) != 1 || got[0] != schema.EventAgentEnd {
		t.Errorf("Filter = %v, want [agent_end]", got)
	}
}

func TestAsyncHookConformance(t *testing.T) {
	h, _, _ := newHookWithStore(t)
	var _ hook.AsyncHook = h
}

// failingStore is a minimal vector.VectorStore stub for negative-path
// testing. Add returns the configured error; everything else returns
// vector.ErrNotSupported because tests do not exercise it.
type failingStore struct {
	addCalls atomic.Int32
	addErr   error
}

func (f *failingStore) Add(_ context.Context, _ vector.Document) error {
	f.addCalls.Add(1)
	return f.addErr
}

func (f *failingStore) Search(_ context.Context, _ []float32, _ vector.SearchOptions) ([]vector.SearchHit, error) {
	return nil, vector.ErrNotSupported
}

func (f *failingStore) Delete(_ context.Context, _ string) error { return vector.ErrNotSupported }

func (f *failingStore) List(_ context.Context) ([]vector.Document, error) {
	return nil, vector.ErrNotSupported
}

func TestBuildDocID_Stability(t *testing.T) {
	t1 := time.Now()
	ev := schema.Event{SessionID: "s", AgentID: "a", Timestamp: t1}
	got := buildDocID(ev)
	want := fmt.Sprintf("session=s|agent=a|ts=%d", t1.UnixNano())
	if got != want {
		t.Errorf("buildDocID = %q, want %q", got, want)
	}
}
