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
	"reflect"
	"strings"
	"sync"
	"testing"
)

// capabilityStores used to exercise the checks. Each embeds MapStore so the
// full Store method set is real; only the capability under test is added.

// processLevelStore claims process-local durability — it implements
// DurableStore but must still be rejected by RequireDurableStore.
type processLevelStore struct {
	*MapStore
}

func (s *processLevelStore) Durability() Durability { return DurabilityProcess }

// restartLevelStore claims surviving-restart durability.
type restartLevelStore struct {
	*MapStore
}

func (s *restartLevelStore) Durability() Durability { return DurabilityRestart }

// casStore adds a CompareAndSwap over MapStore. Single-goroutine safe for the
// tests; the mutex keeps the two-check-and-set window honest.
type casStore struct {
	*MapStore
	mu sync.Mutex
}

func (s *casStore) CompareAndSwap(ctx context.Context, key string, expected, desired any, ttl int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok, err := s.Get(ctx, key)
	if err != nil {
		return false, err
	}

	matched := expected == nil && !ok ||
		expected != nil && ok && reflect.DeepEqual(cur, expected)
	if !matched {
		return false, nil
	}

	return true, s.Set(ctx, key, desired, ttl)
}

func TestRequireDurableStore_RejectsMapStore(t *testing.T) {
	err := RequireDurableStore(NewMapStore())
	if !errors.Is(err, ErrStoreNotDurable) {
		t.Fatalf("errors.Is(err, ErrStoreNotDurable) = false, err = %v", err)
	}
	for _, want := range []string{"*memory.MapStore", "memory.DurableStore", "Durability"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

func TestRequireDurableStore_RejectsNil(t *testing.T) {
	if err := RequireDurableStore(nil); !errors.Is(err, ErrNilStore) {
		t.Errorf("nil store should fail with ErrNilStore, got %v", err)
	}
}

func TestRequireDurableStore_RejectsProcessLevelClaim(t *testing.T) {
	err := RequireDurableStore(&processLevelStore{MapStore: NewMapStore()})
	if !errors.Is(err, ErrStoreNotDurable) {
		t.Fatalf("process-level claim should be rejected, got %v", err)
	}
	// The message must distinguish the two failure modes: the backend did
	// implement the interface, but its own claim is too weak.
	if !strings.Contains(err.Error(), "process-local") || !strings.Contains(err.Error(), "survives-restart") {
		t.Errorf("error should name both levels, got: %v", err)
	}
}

func TestRequireDurableStore_AcceptsRestartLevelClaim(t *testing.T) {
	if err := RequireDurableStore(&restartLevelStore{MapStore: NewMapStore()}); err != nil {
		t.Errorf("restart-level claim should pass, got %v", err)
	}
}

func TestRequireAtomicStore_RejectsMapStore(t *testing.T) {
	err := RequireAtomicStore(NewMapStore())
	if !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("errors.Is(err, ErrStoreNotAtomic) = false, err = %v", err)
	}
	for _, want := range []string{"*memory.MapStore", "memory.AtomicStore", "CompareAndSwap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

func TestRequireAtomicStore_RejectsNil(t *testing.T) {
	if err := RequireAtomicStore(nil); !errors.Is(err, ErrNilStore) {
		t.Errorf("nil store should fail with ErrNilStore, got %v", err)
	}
}

func TestRequireAtomicStore_AcceptsCASStore(t *testing.T) {
	if err := RequireAtomicStore(&casStore{MapStore: NewMapStore()}); err != nil {
		t.Errorf("CAS-capable store should pass, got %v", err)
	}
}

func TestManager_WithDurableStore_RejectsMapStore(t *testing.T) {
	opt, err := WithDurableStore(NewMapStore())
	if !errors.Is(err, ErrStoreNotDurable) || opt != nil {
		t.Errorf("WithDurableStore(MapStore) = (%v, %v), want (nil, ErrStoreNotDurable)", opt, err)
	}
}

func TestManager_WithDurableStore_WrapsStoreTier(t *testing.T) {
	opt, err := WithDurableStore(&restartLevelStore{MapStore: NewMapStore()})
	if err != nil {
		t.Fatalf("WithDurableStore: %v", err)
	}

	mgr := NewManager(opt)
	store := mgr.Store()
	if store == nil {
		t.Fatal("store tier should be set")
	}
	if _, ok := store.(*LongTermMemory); !ok {
		t.Errorf("store tier type = %T, want *LongTermMemory", store)
	}

	ctx := context.Background()
	if err := store.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set through wrapped tier: %v", err)
	}
	got, err := store.Get(ctx, "k")
	if err != nil || got != "v" {
		t.Errorf("Get = (%v, %v), want (\"v\", nil)", got, err)
	}
}

func TestManager_WithAtomicStore_RejectsMapStore(t *testing.T) {
	opt, err := WithAtomicStore(NewMapStore())
	if !errors.Is(err, ErrStoreNotAtomic) || opt != nil {
		t.Errorf("WithAtomicStore(MapStore) = (%v, %v), want (nil, ErrStoreNotAtomic)", opt, err)
	}
}

func TestManager_WithAtomicStore_WrapsStoreTier(t *testing.T) {
	opt, err := WithAtomicStore(&casStore{MapStore: NewMapStore()})
	if err != nil {
		t.Fatalf("WithAtomicStore: %v", err)
	}

	mgr := NewManager(WithSession(NewSessionMemory("a", "s")), WithArchiver(ArchiveAll()), opt)
	ctx := context.Background()

	// Archive a session entry through the wrapped tier to prove it is a live
	// store-tier Memory, not just a set field.
	if err := mgr.session.Set(ctx, "fact", "value", 0); err != nil {
		t.Fatalf("session Set: %v", err)
	}
	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore: %v", err)
	}
	got, err := mgr.Store().Get(ctx, "fact")
	if err != nil || got != "value" {
		t.Errorf("store Get = (%v, %v), want (\"value\", nil)", got, err)
	}
}
