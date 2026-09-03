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

package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConstructors_SignaturesUnchanged(t *testing.T) {
	ctx := context.Background()
	store := NewMapStore()

	sess := NewSessionMemory("agent", "sess")
	if sess == nil {
		t.Fatal("NewSessionMemory returned nil")
	}
	sessStore := NewSessionMemoryWithStore(store, "agent", "sess")
	if sessStore == nil {
		t.Fatal("NewSessionMemoryWithStore returned nil")
	}
	ltMem := NewInMemoryLongTermMemory()
	if ltMem == nil {
		t.Fatal("NewInMemoryLongTermMemory returned nil")
	}
	ltStore := NewLongTermMemory(store)
	if ltStore == nil {
		t.Fatal("NewLongTermMemory returned nil")
	}

	_ = sess.Set(ctx, "k", "v", 0)
	_ = ltMem.Set(ctx, "k", "v", 0)
}

func TestManager_ForSession_DoesNotMutateOriginal(t *testing.T) {
	session := NewSessionMemory("a1", "s1")
	comp := NewSlidingWindowCompressor(3)
	mgr := NewManager(WithSession(session), WithCompressor(comp))

	orig := mgr.Session()
	view := mgr.ForSession("a2", "s2")
	if view == nil {
		t.Fatal("ForSession returned nil")
	}
	if mgr.Session() != orig {
		t.Fatal("ForSession mutated the original Manager session")
	}
	if view.Compressor() != comp {
		t.Fatal("view should share the compressor")
	}
	if view.Session() == orig {
		t.Fatal("view session should be rebound, not the original pointer")
	}
}

func TestManager_ForSession_DoesNotStackPrefixes(t *testing.T) {
	ms := NewMapStore()
	session := NewSessionMemoryWithStore(ms, "seed", "seed")
	mgr := NewManager(WithSession(session))
	ctx := context.Background()

	v1 := mgr.ForSession("a1", "s1")
	v2 := v1.ForSession("a2", "s2")

	if err := v2.Session().Set(ctx, "msg:000001", "hello", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := v2.Session().Get(ctx, "msg:000001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hello" {
		t.Fatalf("Get = %v, want hello", got)
	}

	if val, _ := v1.Session().Get(ctx, "msg:000001"); val != nil {
		t.Fatalf("v1 should not see v2 data, got %v", val)
	}

	raw, err := ms.List(ctx, "")
	if err != nil {
		t.Fatalf("List store: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("store keys = %d, want 1", len(raw))
	}
	wantPrefix := "mem:session:" + encodeScopeID("a2") + ":" + encodeScopeID("s2") + ":"
	if !strings.HasPrefix(raw[0].Key, wantPrefix) {
		t.Fatalf("physical key %q missing single prefix %q", raw[0].Key, wantPrefix)
	}
	if strings.Count(raw[0].Key, "mem:session:") != 1 {
		t.Fatalf("stacked prefix in %q", raw[0].Key)
	}
}

func TestManager_ForSession_IsolationAndLogicalKeys(t *testing.T) {
	ms := NewMapStore()
	session := NewSessionMemoryWithStore(ms, "unused", "unused")
	mgr := NewManager(WithSession(session))
	ctx := context.Background()

	v11 := mgr.ForSession("a1", "s1")
	v12 := mgr.ForSession("a1", "s2")
	v21 := mgr.ForSession("a2", "s1")

	if err := v11.Session().Set(ctx, "msg:000001", "from-a1-s1", 0); err != nil {
		t.Fatalf("s1 Set: %v", err)
	}
	if err := v12.Session().Set(ctx, "msg:000001", "from-a1-s2", 0); err != nil {
		t.Fatalf("s2 Set: %v", err)
	}
	if err := v21.Session().Set(ctx, "msg:000001", "from-a2-s1", 0); err != nil {
		t.Fatalf("a2 Set: %v", err)
	}

	assertSelfRead := func(t *testing.T, view *Manager, want string) {
		t.Helper()
		got, err := view.Session().Get(ctx, "msg:000001")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != want {
			t.Fatalf("Get = %v, want %q", got, want)
		}
		entries, err := view.Session().List(ctx, "msg:")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("List len = %d, want 1", len(entries))
		}
		if entries[0].Key != "msg:000001" {
			t.Fatalf("logical List key = %q, leaked physical prefix", entries[0].Key)
		}
		if entries[0].AgentID != view.session.(*SessionMemory).agentID {
			t.Fatalf("Entry.AgentID = %q", entries[0].AgentID)
		}
		if entries[0].SessionID != view.session.(*SessionMemory).sessionID {
			t.Fatalf("Entry.SessionID = %q", entries[0].SessionID)
		}
	}

	assertSelfRead(t, v11, "from-a1-s1")
	assertSelfRead(t, v12, "from-a1-s2")
	assertSelfRead(t, v21, "from-a2-s1")

	if val, _ := v12.Session().Get(ctx, "msg:000001"); val != "from-a1-s2" {
		t.Fatalf("s2 Get = %v", val)
	}
	if val, _ := v11.Session().Get(ctx, "msg:000001"); val == "from-a1-s2" {
		t.Fatal("s1 leaked s2 value")
	}

	raw, err := ms.List(ctx, "")
	if err != nil {
		t.Fatalf("store List: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("store keys = %d, want 3", len(raw))
	}
	for _, se := range raw {
		parts := strings.SplitN(se.Key, ":", 5)
		if len(parts) != 5 || parts[0] != "mem" || parts[1] != "session" {
			t.Fatalf("physical key %q: want mem:session:<b64>:<b64>:<logical>", se.Key)
		}
		if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
			t.Fatalf("agent segment %q is not raw base64url: %v", parts[2], err)
		}
		if _, err := base64.RawURLEncoding.DecodeString(parts[3]); err != nil {
			t.Fatalf("session segment %q is not raw base64url: %v", parts[3], err)
		}
		if strings.Contains(parts[2], "=") || strings.Contains(parts[3], "=") {
			t.Fatalf("padded base64url in %q", se.Key)
		}
		if parts[4] != "msg:000001" {
			t.Fatalf("logical tail = %q, want msg:000001", parts[4])
		}
	}
}

func TestManager_ForSession_CRUDAndPromoteSymmetric(t *testing.T) {
	ms := NewMapStore()
	session := NewSessionMemoryWithStore(ms, "seed", "seed")
	mgr := NewManager(WithSession(session))
	ctx := context.Background()
	view := mgr.ForSession("agent", "sess")

	if err := view.Session().Set(ctx, "k1", "v1", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := view.Session().Get(ctx, "k1")
	if err != nil || got != "v1" {
		t.Fatalf("Get = %v, %v", got, err)
	}

	if err := view.Session().BatchSet(ctx, map[string]any{"k2": "v2", "k3": "v3"}, 0); err != nil {
		t.Fatalf("BatchSet: %v", err)
	}
	batch, err := view.Session().BatchGet(ctx, []string{"k1", "k2", "missing"})
	if err != nil {
		t.Fatalf("BatchGet: %v", err)
	}
	if len(batch) != 2 || batch["k1"] != "v1" || batch["k2"] != "v2" {
		t.Fatalf("BatchGet = %#v", batch)
	}
	for k := range batch {
		if strings.HasPrefix(k, "mem:") {
			t.Fatalf("BatchGet leaked physical key %q", k)
		}
	}

	if err := view.Session().Delete(ctx, "k2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if val, _ := view.Session().Get(ctx, "k2"); val != nil {
		t.Fatalf("Get after Delete = %v", val)
	}

	working := NewWorkingMemory("agent", "sess")
	if err := working.Set(ctx, "msg:000010", "promoted", 30); err != nil {
		t.Fatalf("working Set: %v", err)
	}
	if err := view.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession: %v", err)
	}
	promoted, err := view.Session().Get(ctx, "msg:000010")
	if err != nil || promoted != "promoted" {
		t.Fatalf("promoted Get = %v, %v", promoted, err)
	}

	raw, err := ms.List(ctx, "")
	if err != nil {
		t.Fatalf("store List: %v", err)
	}
	for _, se := range raw {
		if strings.Count(se.Key, "mem:session:") != 1 {
			t.Fatalf("promote stacked prefix: %q", se.Key)
		}
		if !strings.HasSuffix(se.Key, ":msg:000010") && !strings.HasSuffix(se.Key, ":k1") && !strings.HasSuffix(se.Key, ":k3") {
			t.Fatalf("unexpected physical key %q", se.Key)
		}
	}

	entries, err := view.Session().List(ctx, "msg:")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "msg:000010" {
		t.Fatalf("List = %+v", entries)
	}
	if entries[0].TTL != 30 {
		t.Fatalf("TTL = %d, want 30", entries[0].TTL)
	}
	if entries[0].AgentID != "agent" || entries[0].SessionID != "sess" {
		t.Fatalf("entry identity = (%q,%q)", entries[0].AgentID, entries[0].SessionID)
	}
}

func TestManager_ForSession_TTLPreserved(t *testing.T) {
	ms := NewMapStore()
	session := NewSessionMemoryWithStore(ms, "seed", "seed")
	view := NewManager(WithSession(session)).ForSession("agent", "sess")
	ctx := context.Background()

	if err := view.Session().Set(ctx, "expiring", "value", 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	sm := view.Session().(*SessionMemory)
	ms.SetCreatedAtForTest(sm.physicalKey("expiring"), time.Now().Add(-2*time.Second))

	got, err := view.Session().Get(ctx, "expiring")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expired Get = %v, want nil", got)
	}
}

func TestEncodeScopeID_RejectsDelimiterCollision(t *testing.T) {
	ms := NewMapStore()
	session := NewSessionMemoryWithStore(ms, "seed", "seed")
	mgr := NewManager(WithSession(session))
	ctx := context.Background()

	left := mgr.ForSession("a:b", "c")
	right := mgr.ForSession("a", "b:c")
	if err := left.Session().Set(ctx, "k", "left", 0); err != nil {
		t.Fatalf("left Set: %v", err)
	}
	if err := right.Session().Set(ctx, "k", "right", 0); err != nil {
		t.Fatalf("right Set: %v", err)
	}
	if got, _ := left.Session().Get(ctx, "k"); got != "left" {
		t.Fatalf("left Get = %v", got)
	}
	if got, _ := right.Session().Get(ctx, "k"); got != "right" {
		t.Fatalf("right Get = %v", got)
	}
}

func TestSyncMemory_Clear_ScopedOnly(t *testing.T) {
	ms := NewMapStore()
	watch := &clearWatchStore{MapStore: ms}
	session := NewSessionMemoryWithStore(watch, "seed", "seed")
	mgr := NewManager(WithSession(session))
	ctx := context.Background()

	v1 := mgr.ForSession("a1", "s1")
	v2 := mgr.ForSession("a1", "s2")
	if err := v1.Session().Set(ctx, "msg:1", "keep-me-not", 0); err != nil {
		t.Fatalf("v1 Set: %v", err)
	}
	if err := v2.Session().Set(ctx, "msg:1", "keep-me", 0); err != nil {
		t.Fatalf("v2 Set: %v", err)
	}
	if err := watch.Set(ctx, "bare:foreign", "untouched", 0); err != nil {
		t.Fatalf("foreign Set: %v", err)
	}

	if err := v1.Session().Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if watch.clearCalls != 0 {
		t.Fatalf("Store.Clear called %d times, want 0", watch.clearCalls)
	}

	if got, _ := v1.Session().Get(ctx, "msg:1"); got != nil {
		t.Fatalf("cleared scope still has %v", got)
	}
	if got, _ := v2.Session().Get(ctx, "msg:1"); got != "keep-me" {
		t.Fatalf("sibling scope Get = %v, want keep-me", got)
	}
	foreign, found, err := watch.Get(ctx, "bare:foreign")
	if err != nil || !found || foreign != "untouched" {
		t.Fatalf("foreign key: found=%v val=%v err=%v", found, foreign, err)
	}
}

func TestSyncMemory_Clear_ListErrorAborts(t *testing.T) {
	ms := NewMapStore()
	st := &listErrStore{MapStore: ms, err: errors.New("list boom")}
	session := NewSessionMemoryWithStore(st, "a", "s")
	ctx := context.Background()
	if err := session.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	err := session.Clear(ctx)
	if err == nil || !strings.Contains(err.Error(), "list boom") {
		t.Fatalf("Clear err = %v, want list boom", err)
	}
	got, _ := session.Get(ctx, "k")
	if got != "v" {
		t.Fatalf("List failure must not delete; Get = %v", got)
	}
}

func TestSyncMemory_Clear_DeleteErrorPropagates(t *testing.T) {
	ms := NewMapStore()
	st := &deleteErrStore{MapStore: ms, err: errors.New("delete boom")}
	session := NewSessionMemoryWithStore(st, "a", "s")
	ctx := context.Background()
	if err := session.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	err := session.Clear(ctx)
	if err == nil || !strings.Contains(err.Error(), "delete boom") {
		t.Fatalf("Clear err = %v, want delete boom", err)
	}
	if len(st.deleted) == 0 {
		t.Fatal("expected at least one Delete attempt")
	}
}

type clearWatchStore struct {
	*MapStore
	clearCalls int
}

func (s *clearWatchStore) Clear(ctx context.Context) error {
	s.clearCalls++
	return s.MapStore.Clear(ctx)
}

type listErrStore struct {
	*MapStore
	err error
}

func (s *listErrStore) List(ctx context.Context, prefix string) ([]StoreEntry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.MapStore.List(ctx, prefix)
}

type deleteErrStore struct {
	*MapStore
	err     error
	deleted []string
}

func (s *deleteErrStore) Delete(ctx context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	if s.err != nil {
		return s.err
	}
	return s.MapStore.Delete(ctx, key)
}
