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

// LongTermMemory is the cross-session ("store") memory tier: facts written
// here outlive a single Run and a single session.
//
// "Long-term" is a tier property, not a storage guarantee. Whether the facts
// survive a process restart is decided entirely by the injected Store — the
// default in-memory backend does not. Assemble the tier through a capability
// check (RequireDurableStore, or Manager's WithDurableStore option) whenever
// a component depends on writes surviving a restart.
//
// It is safe for concurrent use.
type LongTermMemory struct {
	syncMemory
}

// Compile-time check: LongTermMemory implements Memory.
var _ Memory = (*LongTermMemory)(nil)

// NewInMemoryLongTermMemory creates a LongTermMemory backed by an in-memory
// MapStore.
//
// Durability: none. The MapStore lives in this process's heap, so entries are
// visible across sessions for as long as the process runs and are lost the
// moment it exits — there is no disk, no replication, and no sharing with any
// other process. Use it for tests, single-process deployments, and anywhere
// losing cross-session facts on restart is acceptable. When it is not, inject
// a durable backend via NewLongTermMemory instead.
func NewInMemoryLongTermMemory() *LongTermMemory {
	return NewLongTermMemory(NewMapStore())
}

// NewLongTermMemory creates a LongTermMemory backed by the given Store.
//
// Durability and atomicity are delegated to the backend: this constructor
// accepts any Store on purpose, so that explicitly choosing a non-durable
// backend stays a legal, expressible choice. It therefore makes no durability
// promise of its own. Callers that do depend on writes surviving a restart
// must say so at assembly time — run RequireDurableStore on the backend, or
// build the Manager tier with WithDurableStore, both of which fail fast on a
// backend that lacks the capability.
func NewLongTermMemory(store Store) *LongTermMemory {
	return &LongTermMemory{syncMemory: syncMemory{
		mu: new(sync.Mutex),
		memoryBase: memoryBase{
			store: store,
			scope: ScopeStore,
		},
	}}
}
