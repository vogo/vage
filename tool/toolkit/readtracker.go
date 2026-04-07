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

package toolkit

import "sync"

// ReadTracker tracks which file paths have been read.
type ReadTracker interface {
	HasRead(path string) bool
	RecordRead(path string)
}

// MemoryReadTracker is an in-memory implementation of ReadTracker.
// It is safe for concurrent use.
type MemoryReadTracker struct {
	mu         sync.RWMutex
	paths      map[string]bool
	maxEntries int
}

// NewMemoryReadTracker creates a new MemoryReadTracker. When maxEntries is
// reached, the map is cleared (simple eviction) to bound memory in
// long-running sessions. A maxEntries of 0 means unlimited.
func NewMemoryReadTracker(maxEntries int) *MemoryReadTracker {
	return &MemoryReadTracker{
		paths:      make(map[string]bool),
		maxEntries: maxEntries,
	}
}

// HasRead reports whether the given path has been recorded.
func (m *MemoryReadTracker) HasRead(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.paths[path]
}

// RecordRead records that the given path has been read. If the tracker has
// reached maxEntries, the map is cleared before recording the new entry.
func (m *MemoryReadTracker) RecordRead(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.maxEntries > 0 && len(m.paths) >= m.maxEntries {
		m.paths = make(map[string]bool)
	}

	m.paths[path] = true
}
