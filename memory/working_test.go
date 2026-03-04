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
	"testing"
	"time"
)

func TestWorkingMemory_SetGet(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	if err := m.Set(ctx, "key1", "value1", 0); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	val, err := m.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if val != "value1" {
		t.Errorf("Get = %v, want %q", val, "value1")
	}
}

func TestWorkingMemory_GetMissing(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	val, err := m.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if val != nil {
		t.Errorf("Get = %v, want nil", val)
	}
}

func TestWorkingMemory_Delete(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "key1", "value1", 0)
	_ = m.Delete(ctx, "key1")

	val, _ := m.Get(ctx, "key1")
	if val != nil {
		t.Errorf("Get after Delete = %v, want nil", val)
	}
}

func TestWorkingMemory_List(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "msg:1", "hello", 0)
	_ = m.Set(ctx, "msg:2", "world", 0)
	_ = m.Set(ctx, "meta:x", "data", 0)

	entries, err := m.List(ctx, "msg:")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List len = %d, want 2", len(entries))
	}

	for _, e := range entries {
		if e.Scope != ScopeWorking {
			t.Errorf("entry scope = %q, want %q", e.Scope, ScopeWorking)
		}
		if e.AgentID != "agent-1" {
			t.Errorf("entry agentID = %q, want %q", e.AgentID, "agent-1")
		}
	}
}

func TestWorkingMemory_Clear(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "key1", "value1", 0)
	_ = m.Set(ctx, "key2", "value2", 0)

	if err := m.Clear(ctx); err != nil {
		t.Fatalf("Clear error: %v", err)
	}

	entries, _ := m.List(ctx, "")
	if len(entries) != 0 {
		t.Errorf("List after Clear len = %d, want 0", len(entries))
	}
}

func TestWorkingMemory_BatchSetGet(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	batch := map[string]any{"a": 1, "b": 2, "c": 3}
	if err := m.BatchSet(ctx, batch, 0); err != nil {
		t.Fatalf("BatchSet error: %v", err)
	}

	result, err := m.BatchGet(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatalf("BatchGet error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("BatchGet len = %d, want 2", len(result))
	}
	if result["a"] != 1 {
		t.Errorf("BatchGet[a] = %v, want 1", result["a"])
	}
	if result["b"] != 2 {
		t.Errorf("BatchGet[b] = %v, want 2", result["b"])
	}
}

func TestWorkingMemory_TTLExpiry(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	// Set with 1-second TTL, then backdate the entry.
	_ = m.Set(ctx, "expiring", "value", 1)
	m.entries["expiring"].CreatedAt = time.Now().Add(-2 * time.Second)

	val, _ := m.Get(ctx, "expiring")
	if val != nil {
		t.Errorf("Get expired entry = %v, want nil", val)
	}

	// Verify it was deleted (lazy expiry).
	if _, ok := m.entries["expiring"]; ok {
		t.Error("expired entry should be deleted from map")
	}
}

func TestWorkingMemory_TTLExpiryList(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "alive", "yes", 0)
	_ = m.Set(ctx, "dead", "no", 1)
	m.entries["dead"].CreatedAt = time.Now().Add(-2 * time.Second)

	entries, _ := m.List(ctx, "")
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1", len(entries))
	}
	if entries[0].Key != "alive" {
		t.Errorf("entry key = %q, want %q", entries[0].Key, "alive")
	}
}

func TestWorkingMemory_TTLExpiryBatchGet(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "alive", "yes", 0)
	_ = m.Set(ctx, "dead", "no", 1)
	m.entries["dead"].CreatedAt = time.Now().Add(-2 * time.Second)

	result, _ := m.BatchGet(ctx, []string{"alive", "dead"})
	if len(result) != 1 {
		t.Fatalf("BatchGet len = %d, want 1", len(result))
	}
	if result["alive"] != "yes" {
		t.Errorf("BatchGet[alive] = %v, want %q", result["alive"], "yes")
	}
}

func TestWorkingMemory_ContextCanceled(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.Get(ctx, "key"); err == nil {
		t.Error("expected error for canceled context on Get")
	}
	if err := m.Set(ctx, "key", "val", 0); err == nil {
		t.Error("expected error for canceled context on Set")
	}
	if err := m.Delete(ctx, "key"); err == nil {
		t.Error("expected error for canceled context on Delete")
	}
	if _, err := m.List(ctx, ""); err == nil {
		t.Error("expected error for canceled context on List")
	}
	if err := m.Clear(ctx); err == nil {
		t.Error("expected error for canceled context on Clear")
	}
	if _, err := m.BatchGet(ctx, []string{"key"}); err == nil {
		t.Error("expected error for canceled context on BatchGet")
	}
	if err := m.BatchSet(ctx, map[string]any{"key": "val"}, 0); err == nil {
		t.Error("expected error for canceled context on BatchSet")
	}
}

func TestWorkingMemory_EntryMetadata(t *testing.T) {
	m := NewWorkingMemory("agent-1", "sess-1")
	ctx := context.Background()

	_ = m.Set(ctx, "key1", "value1", 60)

	entries, _ := m.List(ctx, "")
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1", len(entries))
	}

	e := entries[0]
	if e.Key != "key1" {
		t.Errorf("Key = %q, want %q", e.Key, "key1")
	}
	if e.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "sess-1")
	}
	if e.TTL != 60 {
		t.Errorf("TTL = %d, want 60", e.TTL)
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}
