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
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWithSessionID_Empty(t *testing.T) {
	ctx := context.Background()
	out := WithSessionID(ctx, "")
	if out != ctx {
		t.Fatalf("WithSessionID with empty string must return the original ctx")
	}
	if got := SessionIDFromContext(out); got != "" {
		t.Fatalf("expected empty sessionID, got %q", got)
	}
}

func TestWithSessionID_RoundTrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "sess-abc")
	if got := SessionIDFromContext(ctx); got != "sess-abc" {
		t.Fatalf("expected %q, got %q", "sess-abc", got)
	}
}

func TestWithEmitter_Nil(t *testing.T) {
	ctx := context.Background()
	out := WithEmitter(ctx, nil)
	if out != ctx {
		t.Fatalf("WithEmitter(nil) must return the original ctx")
	}
	if EmitterFromContext(out) != nil {
		t.Fatalf("expected nil emitter")
	}
}

func TestWithEmitter_RoundTrip(t *testing.T) {
	var captured Event
	em := Emitter(func(e Event) error {
		captured = e
		return nil
	})
	ctx := WithEmitter(context.Background(), em)
	got := EmitterFromContext(ctx)
	if got == nil {
		t.Fatalf("expected emitter to be set")
	}
	_ = got(Event{Type: "x"})
	if captured.Type != "x" {
		t.Fatalf("captured event not delivered through emitter; got %q", captured.Type)
	}
}

func TestMissingValues_ReturnZero(t *testing.T) {
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Fatalf("expected empty sessionID for bare ctx, got %q", got)
	}
	if got := EmitterFromContext(ctx); got != nil {
		t.Fatalf("expected nil emitter for bare ctx")
	}
}

// TestEmitCustomData_EmitsOnce covers the happy path: exactly one EventCustom,
// typed CustomEventData, SessionID inherited from ctx and AgentID left empty
// (the context carries no agent identity to synthesize it from).
func TestEmitCustomData_EmitsOnce(t *testing.T) {
	var got []Event
	ctx := WithEmitter(
		WithSessionID(context.Background(), "sess-custom"),
		func(e Event) error {
			got = append(got, e)
			return nil
		},
	)

	before := time.Now()
	payload := map[string]any{"stage": "parse", "pct": 40}
	EmitCustomData(ctx, "document.parse.progress", payload)
	after := time.Now()

	if len(got) != 1 {
		t.Fatalf("emitted %d events, want exactly 1", len(got))
	}
	e := got[0]
	if e.Type != EventCustom {
		t.Errorf("Type = %q, want %q", e.Type, EventCustom)
	}
	if e.SessionID != "sess-custom" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "sess-custom")
	}
	if e.AgentID != "" {
		t.Errorf("AgentID = %q, want empty", e.AgentID)
	}
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Errorf("Timestamp %v not in range [%v, %v]", e.Timestamp, before, after)
	}

	data, ok := e.Data.(CustomEventData)
	if !ok {
		t.Fatalf("Data type = %T, want CustomEventData", e.Data)
	}
	if data.Name != "document.parse.progress" {
		t.Errorf("Name = %q, want %q", data.Name, "document.parse.progress")
	}
	m, ok := data.Payload.(map[string]any)
	if !ok {
		t.Fatalf("Payload type = %T, want map[string]any", data.Payload)
	}
	if m["stage"] != "parse" || m["pct"] != 40 {
		t.Errorf("Payload = %v, want stage=parse pct=40", m)
	}
}

// TestEmitCustomData_NoEmitter pins the documented no-op: a tool calling this
// under an executor that never bound an emitter must not panic or error, so
// the same handler stays reusable outside a streaming agent.
func TestEmitCustomData_NoEmitter(t *testing.T) {
	EmitCustomData(context.Background(), "some.name", "payload")
}

// TestEmitCustomData_EmitterError asserts the emitter's error is swallowed:
// a closing or cancelled stream drops the auxiliary event instead of
// surfacing anything into the tool's hot path.
func TestEmitCustomData_EmitterError(t *testing.T) {
	calls := 0
	ctx := WithEmitter(context.Background(), func(Event) error {
		calls++
		return fmt.Errorf("stream closed")
	})

	EmitCustomData(ctx, "some.name", nil)

	if calls != 1 {
		t.Fatalf("emitter called %d times, want 1", calls)
	}
}

// TestEmitCustomData_NoSessionID documents that SessionID is inherited, not
// required — an emitter without WithSessionID still receives the event.
func TestEmitCustomData_NoSessionID(t *testing.T) {
	var got Event
	ctx := WithEmitter(context.Background(), func(e Event) error {
		got = e
		return nil
	})

	EmitCustomData(ctx, "n", 1)

	if got.Type != EventCustom {
		t.Fatalf("Type = %q, want %q", got.Type, EventCustom)
	}
	if got.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", got.SessionID)
	}
}

func TestRunValues_SetGetRoundTrip(t *testing.T) {
	ctx := WithRunValues(context.Background())

	if !SetRunValue(ctx, "k", 42) {
		t.Fatalf("SetRunValue must report true when a store is bound")
	}

	v, ok := GetRunValue(ctx, "k")
	if !ok {
		t.Fatalf("expected key %q to be present", "k")
	}
	if v != 42 {
		t.Fatalf("value = %v, want 42", v)
	}
}

func TestRunValues_Overwrite(t *testing.T) {
	ctx := WithRunValues(context.Background())

	SetRunValue(ctx, "k", "first")
	if !SetRunValue(ctx, "k", "second") {
		t.Fatalf("overwrite must report true")
	}

	v, ok := GetRunValue(ctx, "k")
	if !ok || v != "second" {
		t.Fatalf("value = %v (ok=%v), want %q", v, ok, "second")
	}
}

func TestRunValues_MissingKey(t *testing.T) {
	ctx := WithRunValues(context.Background())
	SetRunValue(ctx, "present", 1)

	v, ok := GetRunValue(ctx, "absent")
	if ok {
		t.Fatalf("expected absent key to report ok=false")
	}
	if v != nil {
		t.Fatalf("expected nil value for absent key, got %v", v)
	}
}

// TestRunValues_NoStore pins the no-op/miss semantics chosen over panic or
// error, so a tool written for TaskAgent stays usable under an executor that
// binds no run values.
func TestRunValues_NoStore(t *testing.T) {
	ctx := context.Background()

	if SetRunValue(ctx, "k", 1) {
		t.Fatalf("SetRunValue on a bare ctx must report false")
	}

	v, ok := GetRunValue(ctx, "k")
	if ok {
		t.Fatalf("GetRunValue on a bare ctx must report false")
	}
	if v != nil {
		t.Fatalf("GetRunValue on a bare ctx must return nil, got %v", v)
	}
}

// TestRunValues_ShadowsParentStore covers the run-boundary rule: a nested run
// binding its own store must neither see nor corrupt the parent's values.
func TestRunValues_ShadowsParentStore(t *testing.T) {
	parent := WithRunValues(context.Background())
	SetRunValue(parent, "k", "parent")

	child := WithRunValues(parent)
	if _, ok := GetRunValue(child, "k"); ok {
		t.Fatalf("a freshly bound store must start empty")
	}

	SetRunValue(child, "k", "child")

	if v, _ := GetRunValue(parent, "k"); v != "parent" {
		t.Fatalf("parent value = %v, want %q", v, "parent")
	}
}

// TestRunValues_Concurrent exercises the parallel-tool access pattern; run
// under -race it guards the sync.Map contract.
func TestRunValues_Concurrent(t *testing.T) {
	ctx := WithRunValues(context.Background())

	const workers = 16

	var wg sync.WaitGroup

	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()

			SetRunValue(ctx, fmt.Sprintf("k-%d", i), i)
			SetRunValue(ctx, "shared", i)
			_, _ = GetRunValue(ctx, "shared")
			_, _ = GetRunValue(ctx, fmt.Sprintf("k-%d", (i+1)%workers))
		}(i)
	}

	wg.Wait()

	for i := range workers {
		v, ok := GetRunValue(ctx, fmt.Sprintf("k-%d", i))
		if !ok || v != i {
			t.Fatalf("k-%d = %v (ok=%v), want %d", i, v, ok, i)
		}
	}

	// The shared key has no promised winner — only presence is contractual.
	if _, ok := GetRunValue(ctx, "shared"); !ok {
		t.Fatalf("expected the contended key to be present")
	}
}
