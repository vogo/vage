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

// SessionMemory is a per-session in-process memory store.
// It is safe for concurrent use. Immutable after construction.
type SessionMemory struct {
	mu sync.Mutex
	mapStore
}

// Compile-time check: SessionMemory implements Memory.
var _ Memory = (*SessionMemory)(nil)

// NewSessionMemory creates a new SessionMemory for a session.
func NewSessionMemory(agentID, sessionID string) *SessionMemory {
	return &SessionMemory{mapStore: mapStore{
		entries:   make(map[string]*Entry),
		scope:     ScopeSession,
		agentID:   agentID,
		sessionID: sessionID,
	}}
}

func (m *SessionMemory) Get(ctx context.Context, key string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	v, _ := m.get(key)

	return v, nil
}

func (m *SessionMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.set(key, value, ttl)

	return nil
}

func (m *SessionMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.del(key)

	return nil
}

func (m *SessionMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.list(prefix), nil
}

func (m *SessionMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.clear()

	return nil
}

func (m *SessionMemory) BatchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.batchGet(keys), nil
}

func (m *SessionMemory) BatchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.batchSet(entries, ttl)

	return nil
}
