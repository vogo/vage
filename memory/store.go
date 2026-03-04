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

// StoreMemory is a cross-session persistent in-process memory store (MVP).
// It is safe for concurrent use. Immutable after construction.
type StoreMemory struct {
	mu sync.Mutex
	mapStore
}

// Compile-time check: StoreMemory implements Memory.
var _ Memory = (*StoreMemory)(nil)

// NewStoreMemory creates a new StoreMemory.
func NewStoreMemory() *StoreMemory {
	return &StoreMemory{mapStore: mapStore{
		entries: make(map[string]*Entry),
		scope:   ScopeStore,
	}}
}

func (m *StoreMemory) Get(ctx context.Context, key string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	v, _ := m.get(key)

	return v, nil
}

func (m *StoreMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.set(key, value, ttl)

	return nil
}

func (m *StoreMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.del(key)

	return nil
}

func (m *StoreMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.list(prefix), nil
}

func (m *StoreMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.clear()

	return nil
}

func (m *StoreMemory) BatchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.batchGet(keys), nil
}

func (m *StoreMemory) BatchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.batchSet(entries, ttl)

	return nil
}
