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
	"errors"
	"testing"
)

func TestNewManager_Defaults(t *testing.T) {
	mgr := NewManager()

	if mgr.Session() != nil {
		t.Error("Session should be nil by default")
	}
	if mgr.Store() != nil {
		t.Error("Store should be nil by default")
	}
	if mgr.Compressor() != nil {
		t.Error("Compressor should be nil by default")
	}
	if mgr.promoter == nil {
		t.Error("promoter should not be nil (default PromoteAll)")
	}
	if mgr.archiver == nil {
		t.Error("archiver should not be nil (default ArchiveNone)")
	}
}

func TestNewManager_WithOptions(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	store := NewStoreMemory()
	compressor := NewSlidingWindowCompressor(10)

	mgr := NewManager(
		WithSession(session),
		WithStore(store),
		WithCompressor(compressor),
		WithPromoter(PromoteNone()),
		WithArchiver(ArchiveAll()),
	)

	if mgr.Session() != session {
		t.Error("Session not set correctly")
	}
	if mgr.Store() != store {
		t.Error("Store not set correctly")
	}
	if mgr.Compressor() != compressor {
		t.Error("Compressor not set correctly")
	}
}

func TestManager_NewWorkingMemory(t *testing.T) {
	_ = NewManager() // ensure manager creation works
	wm := NewWorkingMemory("agent-1", "sess-1")

	if wm == nil {
		t.Fatal("NewWorkingMemory returned nil")
	}
	if wm.agentID != "agent-1" {
		t.Errorf("agentID = %q, want %q", wm.agentID, "agent-1")
	}
	if wm.sessionID != "sess-1" {
		t.Errorf("sessionID = %q, want %q", wm.sessionID, "sess-1")
	}
}

func TestManager_PromoteToSession(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	mgr := NewManager(WithSession(session))

	ctx := context.Background()
	working := NewWorkingMemory("agent-1", "sess-1")
	_ = working.Set(ctx, "msg:1", "hello", 0)
	_ = working.Set(ctx, "msg:2", "world", 0)

	if err := mgr.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession error: %v", err)
	}

	// Verify entries are in session.
	val, _ := session.Get(ctx, "msg:1")
	if val != "hello" {
		t.Errorf("session msg:1 = %v, want %q", val, "hello")
	}

	val, _ = session.Get(ctx, "msg:2")
	if val != "world" {
		t.Errorf("session msg:2 = %v, want %q", val, "world")
	}
}

func TestManager_PromoteToSession_NoSession(t *testing.T) {
	mgr := NewManager() // no session configured

	ctx := context.Background()
	working := NewWorkingMemory("agent-1", "sess-1")
	_ = working.Set(ctx, "key1", "value1", 0)

	// Should be a no-op, not an error.
	if err := mgr.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession error: %v", err)
	}
}

func TestManager_PromoteToSession_PromoteNone(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	mgr := NewManager(
		WithSession(session),
		WithPromoter(PromoteNone()),
	)

	ctx := context.Background()
	working := NewWorkingMemory("agent-1", "sess-1")
	_ = working.Set(ctx, "key1", "value1", 0)

	if err := mgr.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession error: %v", err)
	}

	// Nothing should be promoted.
	entries, _ := session.List(ctx, "")
	if len(entries) != 0 {
		t.Errorf("session entries len = %d, want 0", len(entries))
	}
}

func TestManager_PromoteToSession_EmptyWorking(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	mgr := NewManager(WithSession(session))

	ctx := context.Background()
	working := NewWorkingMemory("agent-1", "sess-1")

	if err := mgr.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession error: %v", err)
	}
}

func TestManager_PromoteToSession_PromoterError(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	errPromoter := PromoteFunc(func(_ context.Context, _ []Entry) ([]Entry, error) {
		return nil, errors.New("promote failed")
	})

	mgr := NewManager(
		WithSession(session),
		WithPromoter(errPromoter),
	)

	ctx := context.Background()
	working := NewWorkingMemory("agent-1", "sess-1")
	_ = working.Set(ctx, "key1", "value1", 0)

	err := mgr.PromoteToSession(ctx, working)
	if err == nil {
		t.Fatal("expected error from promoter")
	}
	if err.Error() != "memory: promote entries: promote failed" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestManager_ArchiveToStore(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	store := NewStoreMemory()
	mgr := NewManager(
		WithSession(session),
		WithStore(store),
		WithArchiver(ArchiveAll()),
	)

	ctx := context.Background()
	_ = session.Set(ctx, "msg:1", "hello", 0)

	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore error: %v", err)
	}

	val, _ := store.Get(ctx, "msg:1")
	if val != "hello" {
		t.Errorf("store msg:1 = %v, want %q", val, "hello")
	}
}

func TestManager_ArchiveToStore_NoStore(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	mgr := NewManager(WithSession(session))

	ctx := context.Background()
	_ = session.Set(ctx, "key1", "value1", 0)

	// Should be a no-op.
	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore error: %v", err)
	}
}

func TestManager_ArchiveToStore_NoSession(t *testing.T) {
	store := NewStoreMemory()
	mgr := NewManager(WithStore(store))

	ctx := context.Background()

	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore error: %v", err)
	}
}

func TestManager_ArchiveToStore_ArchiveNone(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	store := NewStoreMemory()
	mgr := NewManager(
		WithSession(session),
		WithStore(store),
		// default archiver is ArchiveNone
	)

	ctx := context.Background()
	_ = session.Set(ctx, "key1", "value1", 0)

	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore error: %v", err)
	}

	entries, _ := store.List(ctx, "")
	if len(entries) != 0 {
		t.Errorf("store entries len = %d, want 0", len(entries))
	}
}

func TestManager_ArchiveToStore_ArchiverError(t *testing.T) {
	session := NewSessionMemory("agent-1", "sess-1")
	store := NewStoreMemory()
	errArchiver := ArchiveFunc(func(_ context.Context, _ []Entry) ([]Entry, error) {
		return nil, errors.New("archive failed")
	})

	mgr := NewManager(
		WithSession(session),
		WithStore(store),
		WithArchiver(errArchiver),
	)

	ctx := context.Background()
	_ = session.Set(ctx, "key1", "value1", 0)

	err := mgr.ArchiveToStore(ctx)
	if err == nil {
		t.Fatal("expected error from archiver")
	}
	if err.Error() != "memory: archive entries: archive failed" {
		t.Errorf("error = %q", err.Error())
	}
}
