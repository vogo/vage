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
	"time"
)

// Scope identifies the memory tier an entry belongs to.
type Scope string

const (
	// ScopeWorking is per-Run working memory, discarded after each run.
	ScopeWorking Scope = "working"
	// ScopeSession is per-session memory, persists across runs within a session.
	ScopeSession Scope = "session"
	// ScopeStore is cross-session persistent memory.
	ScopeStore Scope = "store"
)

// Entry is a single memory item stored in any tier.
type Entry struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Scope     Scope     `json:"scope"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	TTL       int64     `json:"ttl,omitempty"` // seconds; 0 means no expiry
}

// IsExpired reports whether the entry has exceeded its TTL.
func (e *Entry) IsExpired() bool {
	if e.TTL <= 0 {
		return false
	}
	return time.Since(e.CreatedAt) > time.Duration(e.TTL)*time.Second
}

// Memory is the core interface for a memory tier.
type Memory interface {
	// Get returns the value for the given key, or nil if not found.
	Get(ctx context.Context, key string) (any, error)
	// Set stores a value under the given key with an optional TTL in seconds.
	Set(ctx context.Context, key string, value any, ttl int64) error
	// Delete removes the entry for the given key.
	Delete(ctx context.Context, key string) error
	// List returns all entries whose keys have the given prefix.
	List(ctx context.Context, prefix string) ([]Entry, error)
	// Clear removes all entries.
	Clear(ctx context.Context) error
	// BatchGet returns the values for the given keys.
	BatchGet(ctx context.Context, keys []string) (map[string]any, error)
	// BatchSet stores multiple key-value pairs with the given TTL.
	BatchSet(ctx context.Context, entries map[string]any, ttl int64) error
}

// SearchableMemory extends Memory with search and indexing capabilities.
type SearchableMemory interface {
	Memory
	// Search finds entries matching the query string.
	Search(ctx context.Context, query string, limit int) ([]Entry, error)
	// Index indexes an entry for search.
	Index(ctx context.Context, entry Entry) error
}
