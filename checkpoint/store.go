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

package checkpoint

import "context"

// IterationStore persists per-iteration checkpoints written by TaskAgent
// at the end of each ReAct loop iteration, so that Run can be resumed
// across crashes / restarts.
//
// The store is named IterationStore (not "CheckpointStore") to avoid
// confusion with vage/orchestrate.CheckpointStore, which is a separate
// store for DAG-level checkpoints keyed by (dagID, nodeID).
//
// Implementations must be safe for concurrent use. Sequence is strictly
// monotonic per SessionID — the store guards this invariant under its
// own lock so concurrent Save calls on the same session are serialized.
type IterationStore interface {
	// Save persists cp. The store assigns Sequence and ID; any
	// caller-supplied values for these fields are overwritten. Save
	// also stamps cp.CreatedAt to time.Now() when zero.
	//
	// After Save returns nil, cp.Sequence / cp.ID / cp.CreatedAt are
	// populated; the caller can read them to emit hook events.
	//
	// Returns ErrInvalidArgument when cp is nil or cp.SessionID is
	// empty.
	Save(ctx context.Context, cp *Checkpoint) error

	// Load returns the checkpoint identified by sessionID and id.
	// id == "" means "the latest by Sequence". sessionID is required;
	// passing an empty session id returns ErrInvalidArgument.
	//
	// Returns ErrCheckpointNotFound when no checkpoint matches.
	Load(ctx context.Context, sessionID, id string) (*Checkpoint, error)

	// List returns metadata for every checkpoint of sessionID in
	// ascending Sequence order. An empty session returns ([], nil).
	List(ctx context.Context, sessionID string) ([]*CheckpointMeta, error)

	// Delete removes every checkpoint for sessionID. Idempotent on
	// unknown id.
	Delete(ctx context.Context, sessionID string) error
}
