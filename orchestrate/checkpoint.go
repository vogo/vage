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

package orchestrate

import (
	"context"
	"maps"
	"sync"

	"github.com/vogo/largemodel/schema"
)

// CheckpointStore persists node results for resume and replay.
type CheckpointStore interface {
	// Save persists a node's result.
	Save(ctx context.Context, dagID, nodeID string, resp *schema.RunResponse) error
	// Load retrieves a single node's result.
	Load(ctx context.Context, dagID, nodeID string) (*schema.RunResponse, error)
	// LoadAll retrieves all saved node results for a DAG execution.
	LoadAll(ctx context.Context, dagID string) (map[string]*schema.RunResponse, error)
	// Clear removes all saved results for a DAG execution.
	Clear(ctx context.Context, dagID string) error
}

// InMemoryCheckpointStore is an in-memory implementation of CheckpointStore.
type InMemoryCheckpointStore struct {
	mu   sync.RWMutex
	data map[string]map[string]*schema.RunResponse // dagID -> nodeID -> response
}

// NewInMemoryCheckpointStore creates a new in-memory checkpoint store.
func NewInMemoryCheckpointStore() *InMemoryCheckpointStore {
	return &InMemoryCheckpointStore{
		data: make(map[string]map[string]*schema.RunResponse),
	}
}

func (s *InMemoryCheckpointStore) Save(_ context.Context, dagID, nodeID string, resp *schema.RunResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[dagID] == nil {
		s.data[dagID] = make(map[string]*schema.RunResponse)
	}
	s.data[dagID][nodeID] = resp
	return nil
}

func (s *InMemoryCheckpointStore) Load(_ context.Context, dagID, nodeID string) (*schema.RunResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if nodes, ok := s.data[dagID]; ok {
		return nodes[nodeID], nil
	}
	return nil, nil
}

func (s *InMemoryCheckpointStore) LoadAll(_ context.Context, dagID string) (map[string]*schema.RunResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes, ok := s.data[dagID]
	if !ok {
		return nil, nil
	}
	result := make(map[string]*schema.RunResponse, len(nodes))
	maps.Copy(result, nodes)
	return result, nil
}

func (s *InMemoryCheckpointStore) Clear(_ context.Context, dagID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, dagID)
	return nil
}
