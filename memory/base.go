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
	"sync"
)

// memoryBase holds the fields and helpers shared by all memory tiers.
// Logical Memory keys are mapped to a physical Store keyspace so
// co-located backends stay partitioned by tier and, for session/working,
// by (agentID, sessionID).
type memoryBase struct {
	store     Store
	scope     Scope
	agentID   string
	sessionID string
}

func (b *memoryBase) get(ctx context.Context, key string) (any, error) {
	v, _, err := b.store.Get(ctx, b.physicalKey(key))
	return v, err
}

func (b *memoryBase) set(ctx context.Context, key string, value any, ttl int64) error {
	return b.store.Set(ctx, b.physicalKey(key), value, ttl)
}

func (b *memoryBase) delete(ctx context.Context, key string) error {
	return b.store.Delete(ctx, b.physicalKey(key))
}

// list converts StoreEntry results into tier-annotated Entry values with
// logical keys restored so the Memory API never leaks the physical prefix.
func (b *memoryBase) list(ctx context.Context, prefix string) ([]Entry, error) {
	raw, err := b.store.List(ctx, b.physicalKey(prefix))
	if err != nil {
		return nil, err
	}
	for i := range raw {
		raw[i].Key = b.logicalKey(raw[i].Key)
	}
	return b.toEntries(raw), nil
}

// toEntries converts a slice of StoreEntry to a slice of Entry, stamping
// the tier's scope, agentID, and sessionID.
func (b *memoryBase) toEntries(raw []StoreEntry) []Entry {
	entries := make([]Entry, len(raw))
	for i, se := range raw {
		entries[i] = Entry{
			Key:       se.Key,
			Value:     se.Value,
			Scope:     b.scope,
			AgentID:   b.agentID,
			SessionID: b.sessionID,
			CreatedAt: se.CreatedAt,
			TTL:       se.TTL,
		}
	}
	return entries
}

// batchGet delegates to BatchStore if available, otherwise falls back to
// sequential Get calls. Keys on the Memory API stay logical.
func (b *memoryBase) batchGet(ctx context.Context, keys []string) (map[string]any, error) {
	physical := make([]string, len(keys))
	rev := make(map[string]string, len(keys))
	for i, k := range keys {
		pk := b.physicalKey(k)
		physical[i] = pk
		rev[pk] = k
	}

	var (
		raw map[string]any
		err error
	)
	if bs, ok := b.store.(BatchStore); ok {
		raw, err = bs.BatchGet(ctx, physical)
	} else {
		raw = make(map[string]any, len(keys))
		for _, pk := range physical {
			v, found, gerr := b.store.Get(ctx, pk)
			if gerr != nil {
				return nil, gerr
			}
			if found {
				raw[pk] = v
			}
		}
	}
	if err != nil {
		return nil, err
	}

	result := make(map[string]any, len(raw))
	for pk, v := range raw {
		if lk, ok := rev[pk]; ok {
			result[lk] = v
		} else {
			result[b.logicalKey(pk)] = v
		}
	}
	return result, nil
}

// batchSet delegates to BatchStore if available, otherwise falls back to
// sequential Set calls. Keys on the Memory API stay logical.
func (b *memoryBase) batchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	physical := make(map[string]any, len(entries))
	for key, value := range entries {
		physical[b.physicalKey(key)] = value
	}
	if bs, ok := b.store.(BatchStore); ok {
		return bs.BatchSet(ctx, physical, ttl)
	}
	for key, value := range physical {
		if err := b.store.Set(ctx, key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

// syncMemory wraps memoryBase with a mutex for concurrent use.
// It implements the Memory interface and is embedded by SessionMemory
// and LongTermMemory. mu is a pointer so ForSession views share the
// original synchronisation domain (Store is single-goroutine-safe).
type syncMemory struct {
	mu *sync.Mutex
	memoryBase
}

// Compile-time check: *syncMemory implements Memory.
var _ Memory = (*syncMemory)(nil)

func (m *syncMemory) Get(ctx context.Context, key string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(ctx, key)
}

func (m *syncMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.set(ctx, key, value, ttl)
}

func (m *syncMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.delete(ctx, key)
}

func (m *syncMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(ctx, prefix)
}

// Clear deletes every key in this view's physical prefix. It never calls
// Store.Clear, so sibling scopes and non-memory data on the same backend
// stay intact. A List failure aborts before any Delete; a Delete failure
// is returned without rolling back earlier deletes in this call.
func (m *syncMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	raw, err := m.store.List(ctx, m.keyPrefix())
	if err != nil {
		return err
	}
	for i := range raw {
		if err := m.store.Delete(ctx, raw[i].Key); err != nil {
			return err
		}
	}
	return nil
}

func (m *syncMemory) BatchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.batchGet(ctx, keys)
}

func (m *syncMemory) BatchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.batchSet(ctx, entries, ttl)
}
