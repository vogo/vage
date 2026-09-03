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
type memoryBase struct {
	store     Store
	scope     Scope
	agentID   string
	sessionID string
}

// list converts StoreEntry results into tier-annotated Entry values.
func (b *memoryBase) list(ctx context.Context, prefix string) ([]Entry, error) {
	raw, err := b.store.List(ctx, prefix)
	if err != nil {
		return nil, err
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
// sequential Get calls.
func (b *memoryBase) batchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if bs, ok := b.store.(BatchStore); ok {
		return bs.BatchGet(ctx, keys)
	}
	result := make(map[string]any, len(keys))
	for _, key := range keys {
		v, found, err := b.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if found {
			result[key] = v
		}
	}
	return result, nil
}

// batchSet delegates to BatchStore if available, otherwise falls back to
// sequential Set calls.
func (b *memoryBase) batchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if bs, ok := b.store.(BatchStore); ok {
		return bs.BatchSet(ctx, entries, ttl)
	}
	for key, value := range entries {
		if err := b.store.Set(ctx, key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

// syncMemory wraps memoryBase with a mutex for concurrent use.
// It implements the Memory interface and is embedded by SessionMemory
// and LongTermMemory.
type syncMemory struct {
	mu sync.Mutex
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
	v, _, err := m.store.Get(ctx, key)
	return v, err
}

func (m *syncMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Set(ctx, key, value, ttl)
}

func (m *syncMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Delete(ctx, key)
}

func (m *syncMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(ctx, prefix)
}

func (m *syncMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Clear(ctx)
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
