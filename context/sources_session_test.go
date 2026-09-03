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

package vctx

import (
	"context"
	"fmt"
	"testing"

	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// newSessionMemoryWithMsgs returns a memory.Manager whose session tier has
// been seeded with `count` user messages keyed under "msg:%06d", matching
// the storage convention used by TaskAgent.
func newSessionMemoryWithMsgs(t *testing.T, count int) *memory.Manager {
	t.Helper()

	sess := memory.NewSessionMemory("agent", "session")
	ctx := context.Background()

	for i := range count {
		key := fmt.Sprintf("msg:%06d", i)
		msg := schema.NewUserMessage(schema.ProtocolOpenAIChat, fmt.Sprintf("turn %d", i))
		if err := sess.Set(ctx, key, msg, 0); err != nil {
			t.Fatalf("seed Set: %v", err)
		}
	}

	return memory.NewManager(memory.WithSession(sess))
}

func sessionFetchInput() FetchInput {
	return FetchInput{AgentID: "agent", SessionID: "session"}
}

// TestSessionMemorySource_LoadOrdered verifies messages return in key-sort
// order with OriginalCount populated.
func TestSessionMemorySource_LoadOrdered(t *testing.T) {
	mgr := newSessionMemoryWithMsgs(t, 3)
	src := &SessionMemorySource{Manager: mgr}

	res, err := src.Fetch(context.Background(), sessionFetchInput())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(res.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(res.Messages))
	}
	for i, m := range res.Messages {
		want := fmt.Sprintf("turn %d", i)
		if m.Text() != want {
			t.Errorf("message[%d] = %q, want %q", i, m.Text(), want)
		}
	}
	if res.Report.OriginalCount != 3 {
		t.Errorf("OriginalCount = %d, want 3", res.Report.OriginalCount)
	}
	if res.Report.Status != StatusOK {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusOK)
	}
}

// TestSessionMemorySource_NoManager verifies a nil Manager produces a
// skip — matches the legacy taskagent "no memory configured" branch.
func TestSessionMemorySource_NoManager(t *testing.T) {
	src := &SessionMemorySource{}
	res, err := src.Fetch(context.Background(), sessionFetchInput())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestSessionMemorySource_EmptySession verifies an empty session is a
// skip with OriginalCount = 0 (TaskAgent uses this to start the index at 0).
func TestSessionMemorySource_EmptySession(t *testing.T) {
	mgr := newSessionMemoryWithMsgs(t, 0)
	src := &SessionMemorySource{Manager: mgr}

	res, err := src.Fetch(context.Background(), sessionFetchInput())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if res.Report.OriginalCount != 0 {
		t.Errorf("OriginalCount = %d, want 0", res.Report.OriginalCount)
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestSessionMemorySource_Compressor verifies the session-tier compressor
// is applied to the loaded slice and OriginalCount still reflects the
// pre-compression count.
func TestSessionMemorySource_Compressor(t *testing.T) {
	mgr := newSessionMemoryWithMsgs(t, 5)

	// Wrap a sliding-window compressor that keeps only the last 2.
	comp := memory.NewSlidingWindowCompressor(2)
	mgr2 := memory.NewManager(memory.WithSession(mgr.Session()), memory.WithCompressor(comp))

	src := &SessionMemorySource{Manager: mgr2}
	res, err := src.Fetch(context.Background(), sessionFetchInput())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(res.Messages) != 2 {
		t.Fatalf("compressed messages = %d, want 2", len(res.Messages))
	}
	if res.Report.OriginalCount != 5 {
		t.Errorf("OriginalCount = %d, want 5", res.Report.OriginalCount)
	}
	if res.Report.DroppedN != 3 {
		t.Errorf("DroppedN = %d, want 3", res.Report.DroppedN)
	}
	// Compressor kept the last two: "turn 3" and "turn 4".
	if res.Messages[0].Text() != "turn 3" || res.Messages[1].Text() != "turn 4" {
		t.Errorf("compressor kept wrong slice: %q / %q",
			res.Messages[0].Text(), res.Messages[1].Text())
	}
}

// panicStore fails the test if SessionMemorySource touches the backend
// on a skip path.
type panicStore struct{}

func (panicStore) Get(context.Context, string) (any, bool, error) {
	panic("store.Get")
}

func (panicStore) Set(context.Context, string, any, int64) error { panic("store.Set") }
func (panicStore) Delete(context.Context, string) error          { panic("store.Delete") }
func (panicStore) List(context.Context, string) ([]memory.StoreEntry, error) {
	panic("store.List")
}
func (panicStore) Clear(context.Context) error { panic("store.Clear") }

func TestSessionMemorySource_EmptySessionID_SkippedNoStore(t *testing.T) {
	sess := memory.NewSessionMemoryWithStore(panicStore{}, "agent", "session")
	src := &SessionMemorySource{Manager: memory.NewManager(memory.WithSession(sess))}

	res, err := src.Fetch(context.Background(), FetchInput{AgentID: "agent", SessionID: ""})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
	if len(res.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(res.Messages))
	}
}

func TestSessionMemorySource_ForSession_DoesNotLeak(t *testing.T) {
	shared := memory.NewMapStore()
	sess := memory.NewSessionMemoryWithStore(shared, "unused", "unused")
	mgr := memory.NewManager(memory.WithSession(sess))
	ctx := context.Background()

	if err := mgr.ForSession("agent", "sA").Session().Set(ctx, "msg:000000",
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "secret-from-A"), 0); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := mgr.ForSession("agent", "sB").Session().Set(ctx, "msg:000000",
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "visible-B"), 0); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	src := &SessionMemorySource{Manager: mgr}
	res, err := src.Fetch(ctx, FetchInput{AgentID: "agent", SessionID: "sB"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	if res.Messages[0].Text() != "visible-B" {
		t.Errorf("text = %q, want visible-B", res.Messages[0].Text())
	}
	for _, m := range res.Messages {
		if m.Text() == "secret-from-A" {
			t.Fatal("session B prompt leaked session A history")
		}
	}
}
