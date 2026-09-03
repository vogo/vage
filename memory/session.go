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

import "sync"

// SessionMemory is a per-session in-process memory store.
// It is safe for concurrent use.
type SessionMemory struct {
	syncMemory
}

// Compile-time check: SessionMemory implements Memory.
var _ Memory = (*SessionMemory)(nil)

// sessionRebinder is implemented by SessionMemory so Manager.ForSession can
// rebind identity onto the same Store and mutex without stacking prefixes.
type sessionRebinder interface {
	forSession(agentID, sessionID string) Memory
}

// NewSessionMemory creates a new SessionMemory backed by an in-memory MapStore.
func NewSessionMemory(agentID, sessionID string) *SessionMemory {
	return NewSessionMemoryWithStore(NewMapStore(), agentID, sessionID)
}

// NewSessionMemoryWithStore creates a new SessionMemory backed by the given Store.
func NewSessionMemoryWithStore(store Store, agentID, sessionID string) *SessionMemory {
	return &SessionMemory{syncMemory: syncMemory{
		mu: new(sync.Mutex),
		memoryBase: memoryBase{
			store:     store,
			scope:     ScopeSession,
			agentID:   agentID,
			sessionID: sessionID,
		},
	}}
}

// forSession returns a SessionMemory that shares this instance's Store and
// mutex but uses the given identity. Calling it on a view replaces identity
// rather than nesting prefixes.
func (m *SessionMemory) forSession(agentID, sessionID string) Memory {
	return &SessionMemory{syncMemory: syncMemory{
		mu: m.mu,
		memoryBase: memoryBase{
			store:     m.store,
			scope:     ScopeSession,
			agentID:   agentID,
			sessionID: sessionID,
		},
	}}
}
