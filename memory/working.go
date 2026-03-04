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
	mapStore
}

// Compile-time check: WorkingMemory implements Memory.
var _ Memory = (*WorkingMemory)(nil)

// NewWorkingMemory creates a new WorkingMemory for a single Run.
func NewWorkingMemory(agentID, sessionID string) *WorkingMemory {
	return &WorkingMemory{mapStore{
		entries:   make(map[string]*Entry),
		scope:     ScopeWorking,
		agentID:   agentID,
		sessionID: sessionID,
	}}
}

func (m *WorkingMemory) Get(ctx context.Context, key string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	v, _ := m.get(key)

	return v, nil
}

func (m *WorkingMemory) Set(ctx context.Context, key string, value any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.set(key, value, ttl)

	return nil
}

func (m *WorkingMemory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.del(key)

	return nil
}

func (m *WorkingMemory) List(ctx context.Context, prefix string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return m.list(prefix), nil
}

func (m *WorkingMemory) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.clear()

	return nil
}

func (m *WorkingMemory) BatchGet(ctx context.Context, keys []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return m.batchGet(keys), nil
}

func (m *WorkingMemory) BatchSet(ctx context.Context, entries map[string]any, ttl int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.batchSet(entries, ttl)

	return nil
}
