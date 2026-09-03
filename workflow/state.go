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

package workflow

// fieldIdentity is the merge-time key for a Field. Diagnostic names are for
// errors only; two NewField calls are distinct handles even with the same name.
type fieldIdentity struct {
	name string
}

// Field is the unique typed handle for one readable/writable slot on S.
// Declare each logical field once and reuse the handle; constructing an
// "equivalent" Field elsewhere makes a different identity and will not
// conflict at merge.
//
// Getter/setter must only touch the represented field. Getters for maps,
// slices, or pointers must return an immutable value or a defensive copy:
// Snapshot does not deep-copy S, and mutating through a shared reference
// is a contract violation the scheduler cannot detect.
type Field[S, V any] struct {
	id  *fieldIdentity
	get func(S) V
	set func(*S, V)
}

// NewField constructs a Field. name must be non-empty and is used only in
// conflict diagnostics. get/set must be non-nil. NewField panics on
// programmer error so handles can be package-level variables.
func NewField[S, V any](name string, get func(S) V, set func(*S, V)) Field[S, V] {
	if name == "" {
		panic("workflow: field name must be non-empty")
	}
	if get == nil || set == nil {
		panic("workflow: field getter and setter are required")
	}
	return Field[S, V]{
		id:  &fieldIdentity{name: name},
		get: get,
		set: set,
	}
}

// Snapshot is an opaque read-only view of one committed state version.
// Nodes cannot obtain the scheduler's *S; they read through Get.
type Snapshot[S any] struct {
	state S
}

// Get reads V from snapshot using field. The value is whatever the Field
// getter returns; the scheduler does not wrap or copy it further.
func Get[S, V any](snap Snapshot[S], field Field[S, V]) V {
	if field.get == nil {
		panic("workflow: Get on a zero Field")
	}
	return field.get(snap.state)
}

// Change is one typed field write. Compose changes with NewPatch; there is
// no string-key or any-value write path.
type Change[S any] struct {
	id    *fieldIdentity
	name  string
	apply func(*S)
}

// Set records writing value to field. The setter runs only after the batch
// write-set check succeeds, never during node execution.
func Set[S, V any](field Field[S, V], value V) Change[S] {
	if field.id == nil || field.set == nil {
		panic("workflow: Set on a zero Field")
	}
	return Change[S]{
		id:   field.id,
		name: field.id.name,
		apply: func(s *S) {
			field.set(s, value)
		},
	}
}

// Patch is the set of field writes produced by one node. An empty Patch is
// a read-only node. Patches do not expose arbitrary keys or untyped values.
type Patch[S any] struct {
	changes []Change[S]
}

// NewPatch composes changes into a Patch. Duplicate writes of the same Field
// in one Patch are rejected at merge, before any setter runs.
func NewPatch[S any](changes ...Change[S]) Patch[S] {
	if len(changes) == 0 {
		return Patch[S]{}
	}
	out := make([]Change[S], len(changes))
	copy(out, changes)
	return Patch[S]{changes: out}
}
