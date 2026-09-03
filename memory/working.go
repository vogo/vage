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

import "context"

// WorkingMemory is a per-Run in-process memory store.
// It is not safe for concurrent use (single goroutine per Run).
type WorkingMemory struct {
	memoryBase
}

// Compile-time check: WorkingMemory implements Memory.
var _ Memory = (*WorkingMemory)(nil)

// NewWorkingMemory creates a new WorkingMemory backed by an in-memory MapStore.
func NewWorkingMemory(agentID, sessionID string) *WorkingMemory {
	return NewWorkingMemoryWithStore(NewMapStore(), agentID, sessionID)
}

// NewWorkingMemoryWithStore creates a new WorkingMemory backed by the given Store.
func NewWorkingMemoryWithStore(store Store, agentID, sessionID string) *WorkingMemory {
	return &WorkingMemory{memoryBase{
		store:     store,
		scope:     ScopeWorking,
		agentID:   agentID,
		sessionID: sessionID,
	}}
}

func (m *WorkingMemory) Get(ctx context.Context, key string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.get(ctx, key)
}

func (m *WorkingMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.set(ctx, key, value, ttl)
}

func (m *WorkingMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.delete(ctx, key)
}

func (m *WorkingMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.list(ctx, prefix)
}

func (m *WorkingMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.store.Clear(ctx)
}

func (m *WorkingMemory) BatchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.batchGet(ctx, keys)
}

func (m *WorkingMemory) BatchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.batchSet(ctx, entries, ttl)
}
