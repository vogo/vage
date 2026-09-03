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
	"errors"
	"fmt"
)

// Durability describes how far a backend's writes survive, as reported by the
// backend itself. Levels are ordered and comparable, so a caller can require a
// floor rather than an exact match.
type Durability int

const (
	// DurabilityProcess means writes live in this process's memory only: they
	// are visible across sessions while the process runs and are gone when it
	// exits. This is the level a Store reports when it is honest about being
	// process-local; it does not satisfy RequireDurableStore.
	DurabilityProcess Durability = iota
	// DurabilityRestart means writes survive a restart of this process on the
	// same host — a local file, an embedded database, a co-located service.
	// They do not necessarily survive losing the host.
	DurabilityRestart
	// DurabilityReplicated means writes survive the loss of any single host,
	// having been committed to replicated or otherwise redundant storage.
	DurabilityReplicated
)

// String renders the level for error messages and logs.
func (d Durability) String() string {
	switch d {
	case DurabilityProcess:
		return "process-local"
	case DurabilityRestart:
		return "survives-restart"
	case DurabilityReplicated:
		return "replicated"
	default:
		return fmt.Sprintf("Durability(%d)", int(d))
	}
}

// DurableStore is an optional capability a Store implementation can provide to
// declare how far its writes survive.
//
// The level is self-reported: nothing in this package can verify it, so a
// backend that claims DurabilityRestart is making a promise its own tests and
// reviewers have to keep. Backends that are process-local should either report
// DurabilityProcess or, like MapStore, not implement this interface at all —
// both outcomes are rejected by RequireDurableStore.
type DurableStore interface {
	Store
	// Durability reports the level this backend guarantees for committed
	// writes. It must be constant for the lifetime of the value.
	Durability() Durability
}

// AtomicStore is an optional capability a Store implementation can provide for
// single-key compare-and-swap, the primitive behind idempotent updates, lease
// ownership, and lost-update-free counters.
type AtomicStore interface {
	Store
	// CompareAndSwap sets key to desired only if its current value equals
	// expected, and reports whether the swap happened. A nil expected means
	// "only if the key is absent". The ttl follows Store.Set: seconds, 0 for
	// no expiry.
	//
	// Contract: for concurrent calls on the same key — including calls made
	// through different client instances of the same backend — exactly one
	// swap succeeds, no update is lost, and the outcomes are linearizable.
	// Equality is decided by the backend's own serialised or typed comparison
	// and must be documented by the backend; this package pins the semantics,
	// not the representation.
	CompareAndSwap(ctx context.Context, key string, expected, desired any, ttl int64) (bool, error)
}

// Capability check failures. Wrapped with the offending backend's type so that
// errors.Is keeps working while the message stays actionable.
var (
	// ErrNilStore reports a capability check against a nil Store.
	ErrNilStore = errors.New("memory: store must not be nil")
	// ErrStoreNotDurable reports a backend that does not guarantee writes
	// survive a process restart.
	ErrStoreNotDurable = errors.New("memory: store is not durable")
	// ErrStoreNotAtomic reports a backend without single-key compare-and-swap.
	ErrStoreNotAtomic = errors.New("memory: store is not atomic")
)

// RequireDurableStore reports whether store may back a path that depends on
// writes surviving a process restart, and returns an actionable error if not.
//
// It is stricter than a plain type assertion: the backend must implement
// DurableStore *and* self-report at least DurabilityRestart. A backend that
// implements the interface only to answer DurabilityProcess is declaring
// itself process-local, and is rejected just like one that never implemented
// it — otherwise the level would carry no weight.
//
// Call it at assembly time, so a misconfigured deployment fails while it is
// being wired rather than the first time a restart eats the data.
func RequireDurableStore(store Store) error {
	if store == nil {
		return fmt.Errorf("%w: durability was required here", ErrNilStore)
	}

	ds, ok := store.(DurableStore)
	if !ok {
		return fmt.Errorf("%w: %T does not implement memory.DurableStore; either implement Durability() on %T and report the level its writes actually reach, or wire this path with a backend whose writes survive a process restart", ErrStoreNotDurable, store, store)
	}

	if level := ds.Durability(); level < DurabilityRestart {
		return fmt.Errorf("%w: %T reports %s, below the required %s; either fix the level %T reports if it understates the backend, or wire this path with a backend whose writes survive a process restart", ErrStoreNotDurable, store, level, DurabilityRestart, store)
	}

	return nil
}

// RequireAtomicStore reports whether store may back a path that depends on
// single-key compare-and-swap, and returns an actionable error if not.
//
// Call it at assembly time: a backend without CAS cannot be detected by a
// caller that only ever sees successful Set calls, so the failure would
// otherwise surface as a lost update under concurrency.
func RequireAtomicStore(store Store) error {
	if store == nil {
		return fmt.Errorf("%w: atomicity was required here", ErrNilStore)
	}

	if _, ok := store.(AtomicStore); !ok {
		return fmt.Errorf("%w: %T does not implement memory.AtomicStore; either implement CompareAndSwap() on %T with linearizable single-key semantics, or wire this path with a backend that provides compare-and-swap", ErrStoreNotAtomic, store, store)
	}

	return nil
}
