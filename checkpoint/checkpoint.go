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

// Package checkpoint provides iteration-level snapshots of a TaskAgent
// ReAct loop so that long-running runs can be resumed across crashes,
// SIGTERMs, or process restarts. A Checkpoint is a complete, restorable
// snapshot of one iteration: the message list the next iteration would
// consume, the accumulated token usage, and a Final / StopReason marker
// that tells consumers whether the run terminated.
//
// This package is intentionally independent of vage/session: a session
// is "the fact stream", a checkpoint is "the resume snapshot", and they
// have separate read paths and lifecycles. The two file-system backends
// only happen to share a directory layout for ops convenience.
//
// See vage/orchestrate/checkpoint.go for the unrelated DAG-level
// checkpoint store; the two are addressed differently and serve
// different consumers.
package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// Checkpoint is a complete, restorable snapshot of one ReAct iteration.
//
// Invariants enforced by TaskAgent integration:
//   - Final == false ⇒ StopReason == ""
//   - Final == true  ⇒ StopReason != ""
//   - Sequence is 1-based and strictly monotonic per SessionID;
//     the IterationStore is the source of truth for assignment.
type Checkpoint struct {
	// Identity. Sequence and ID are populated by IterationStore.Save.
	ID       string `json:"id"`
	Sequence int    `json:"sequence"`

	// Addressing.
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id,omitempty"`

	// Position in the ReAct loop.
	Iteration  int               `json:"iteration"`
	Final      bool              `json:"final,omitempty"`
	StopReason schema.StopReason `json:"stop_reason,omitempty"`

	// Restorable state.
	Messages        []aimodel.Message `json:"messages"`
	SessionMsgCount int               `json:"session_msg_count"`
	Usage           aimodel.Usage     `json:"usage"`
	Estimated       bool              `json:"estimated,omitempty"`

	// Audit.
	CreatedAt time.Time `json:"created_at"`
}

// CheckpointMeta is the slim metadata returned by IterationStore.List.
// It never embeds Messages so listing scans stay O(1) per entry.
type CheckpointMeta struct {
	ID            string            `json:"id"`
	Sequence      int               `json:"sequence"`
	SessionID     string            `json:"session_id"`
	AgentID       string            `json:"agent_id,omitempty"`
	Iteration     int               `json:"iteration"`
	Final         bool              `json:"final,omitempty"`
	StopReason    schema.StopReason `json:"stop_reason,omitempty"`
	MessagesCount int               `json:"messages_count"`
	Usage         aimodel.Usage     `json:"usage"`
	CreatedAt     time.Time         `json:"created_at"`
}

// metaFrom projects the slim metadata view from a full Checkpoint.
func metaFrom(cp *Checkpoint) *CheckpointMeta {
	return &CheckpointMeta{
		ID:            cp.ID,
		Sequence:      cp.Sequence,
		SessionID:     cp.SessionID,
		AgentID:       cp.AgentID,
		Iteration:     cp.Iteration,
		Final:         cp.Final,
		StopReason:    cp.StopReason,
		MessagesCount: len(cp.Messages),
		Usage:         cp.Usage,
		CreatedAt:     cp.CreatedAt,
	}
}

// generateID returns an 8-byte hex token suitable as a checkpoint ID.
// Falls back to a timestamp-derived placeholder if crypto/rand fails so
// Save never panics; uniqueness within a session is also guarded by the
// monotonic Sequence prefix in file names.
func generateID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "fb" + hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(buf[:])
}

// cloneMessages copies the top-level slice. aimodel.Message internals
// (Content / ToolCalls) are immutable post-creation by TaskAgent
// convention so a shallow copy is safe.
func cloneMessages(in []aimodel.Message) []aimodel.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]aimodel.Message, len(in))
	copy(out, in)
	return out
}

// cloneCheckpoint returns a defensive copy of cp suitable for handing
// out from Map-backed stores so external mutation cannot bleed back
// into store-internal state.
func cloneCheckpoint(cp *Checkpoint) *Checkpoint {
	if cp == nil {
		return nil
	}
	out := *cp
	out.Messages = cloneMessages(cp.Messages)
	return &out
}
