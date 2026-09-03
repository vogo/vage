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

// This file is the compatibility layer for the memory tier's former name.
// "Persistent" promised something the default backend never delivered: a
// MapStore-backed tier is cross-session but process-local, so callers read
// the old name as "survives a restart" when it does not. The tier is now
// LongTermMemory, and durability is a separate, explicitly checked backend
// capability (see store_capability.go).
//
// Everything below is an alias or a one-line forward, not a second
// implementation: PersistentMemory and LongTermMemory are the same type, so
// values cross the two names freely and existing type assertions keep their
// results. Runtime behaviour is byte-for-byte unchanged.

// PersistentMemory is an alias for LongTermMemory — the same type, not a
// wrapper.
//
// Deprecated: use LongTermMemory. The name overstated the guarantee: the tier
// is cross-session, but durability comes from the injected Store, not from
// this type. Scheduled for removal in the next major version.
type PersistentMemory = LongTermMemory

// NewPersistentMemory forwards to NewInMemoryLongTermMemory.
//
// Deprecated: use NewInMemoryLongTermMemory. Despite the name this never
// persisted anything across a process restart — it returns a MapStore-backed,
// process-local tier. If you need writes to survive a restart, inject a
// durable backend through NewLongTermMemory and gate it with
// RequireDurableStore. Scheduled for removal in the next major version.
func NewPersistentMemory() *LongTermMemory {
	return NewInMemoryLongTermMemory()
}

// NewPersistentMemoryWithStore forwards to NewLongTermMemory.
//
// Deprecated: use NewLongTermMemory. Scheduled for removal in the next major
// version.
func NewPersistentMemoryWithStore(store Store) *LongTermMemory {
	return NewLongTermMemory(store)
}
