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

import (
	"context"
	"errors"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// runStoreContract exercises every IterationStore method and is shared
// between MapIterationStore_test and FileIterationStore_test so the two
// implementations stay byte-for-byte equivalent on observable behavior.
func runStoreContract(t *testing.T, name string, factory func(t *testing.T) IterationStore) {
	t.Helper()

	t.Run(name+"/list_empty_session", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		out, err := s.List(ctx, "sess-empty")
		if err != nil {
			t.Fatalf("List empty: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("List empty: got %d entries, want 0", len(out))
		}
	})

	t.Run(name+"/save_assigns_monotonic_sequence", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		var ids []string
		for i := range 3 {
			cp := newTestCheckpoint("sess-mono", i, false, "")
			if err := s.Save(ctx, cp); err != nil {
				t.Fatalf("Save %d: %v", i, err)
			}
			if cp.Sequence != i+1 {
				t.Errorf("Save %d Sequence = %d, want %d", i, cp.Sequence, i+1)
			}
			if cp.ID == "" {
				t.Errorf("Save %d ID empty", i)
			}
			if cp.CreatedAt.IsZero() {
				t.Errorf("Save %d CreatedAt zero", i)
			}
			ids = append(ids, cp.ID)
		}

		// Verify all ids are unique.
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if seen[id] {
				t.Errorf("duplicate id %q", id)
			}
			seen[id] = true
		}
	})

	t.Run(name+"/sequence_per_session", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		cpA := newTestCheckpoint("sess-A", 0, false, "")
		cpB := newTestCheckpoint("sess-B", 0, false, "")

		if err := s.Save(ctx, cpA); err != nil {
			t.Fatalf("Save A: %v", err)
		}
		if err := s.Save(ctx, cpB); err != nil {
			t.Fatalf("Save B: %v", err)
		}
		if cpA.Sequence != 1 || cpB.Sequence != 1 {
			t.Errorf("per-session Sequence broken: A=%d B=%d, both should be 1",
				cpA.Sequence, cpB.Sequence)
		}
	})

	t.Run(name+"/load_latest_and_by_id", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		var second string
		for i := range 3 {
			cp := newTestCheckpoint("sess-load", i, i == 2, schema.StopReasonComplete)
			if !cp.Final {
				cp.StopReason = ""
			}
			if err := s.Save(ctx, cp); err != nil {
				t.Fatalf("Save %d: %v", i, err)
			}
			if i == 1 {
				second = cp.ID
			}
		}

		latest, err := s.Load(ctx, "sess-load", "")
		if err != nil {
			t.Fatalf("Load latest: %v", err)
		}
		if latest.Sequence != 3 {
			t.Errorf("Load latest Sequence = %d, want 3", latest.Sequence)
		}
		if !latest.Final || latest.StopReason != schema.StopReasonComplete {
			t.Errorf("Load latest Final/StopReason = %v/%q, want true/complete",
				latest.Final, latest.StopReason)
		}

		mid, err := s.Load(ctx, "sess-load", second)
		if err != nil {
			t.Fatalf("Load by id: %v", err)
		}
		if mid.Sequence != 2 {
			t.Errorf("Load by id Sequence = %d, want 2", mid.Sequence)
		}
	})

	t.Run(name+"/load_unknown_id_returns_not_found", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		cp := newTestCheckpoint("sess-nf", 0, false, "")
		if err := s.Save(ctx, cp); err != nil {
			t.Fatalf("Save: %v", err)
		}
		_, err := s.Load(ctx, "sess-nf", "not-an-id")
		if !errors.Is(err, ErrCheckpointNotFound) {
			t.Errorf("Load unknown id err = %v, want ErrCheckpointNotFound", err)
		}
	})

	t.Run(name+"/load_id_from_other_session_returns_not_found", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		a := newTestCheckpoint("sess-X", 0, false, "")
		if err := s.Save(ctx, a); err != nil {
			t.Fatalf("Save A: %v", err)
		}
		b := newTestCheckpoint("sess-Y", 0, false, "")
		if err := s.Save(ctx, b); err != nil {
			t.Fatalf("Save B: %v", err)
		}

		_, err := s.Load(ctx, "sess-X", b.ID)
		if !errors.Is(err, ErrCheckpointNotFound) {
			t.Errorf("Load cross-session id err = %v, want ErrCheckpointNotFound", err)
		}
	})

	t.Run(name+"/load_no_checkpoints_returns_not_found", func(t *testing.T) {
		s := factory(t)
		_, err := s.Load(context.Background(), "sess-empty", "")
		if !errors.Is(err, ErrCheckpointNotFound) {
			t.Errorf("Load empty err = %v, want ErrCheckpointNotFound", err)
		}
	})

	t.Run(name+"/list_returns_meta_in_order", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		for i := range 3 {
			cp := newTestCheckpoint("sess-list", i, false, "")
			if err := s.Save(ctx, cp); err != nil {
				t.Fatalf("Save %d: %v", i, err)
			}
		}
		out, err := s.List(ctx, "sess-list")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(out) != 3 {
			t.Fatalf("List len = %d, want 3", len(out))
		}
		for i, m := range out {
			if m.Sequence != i+1 {
				t.Errorf("List[%d].Sequence = %d, want %d", i, m.Sequence, i+1)
			}
			if m.MessagesCount == 0 {
				t.Errorf("List[%d].MessagesCount = 0, want >0", i)
			}
		}
	})

	t.Run(name+"/delete_clears_session", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		cp := newTestCheckpoint("sess-del", 0, false, "")
		if err := s.Save(ctx, cp); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := s.Delete(ctx, "sess-del"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		out, err := s.List(ctx, "sess-del")
		if err != nil {
			t.Fatalf("List after delete: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("List after delete len = %d, want 0", len(out))
		}
	})

	t.Run(name+"/delete_unknown_session_is_noop", func(t *testing.T) {
		s := factory(t)
		if err := s.Delete(context.Background(), "sess-nope"); err != nil {
			t.Errorf("Delete unknown: %v", err)
		}
	})

	t.Run(name+"/save_validates_inputs", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		if err := s.Save(ctx, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Save nil err = %v, want ErrInvalidArgument", err)
		}
		empty := newTestCheckpoint("", 0, false, "")
		if err := s.Save(ctx, empty); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Save empty session err = %v, want ErrInvalidArgument", err)
		}
		_, err := s.Load(ctx, "", "")
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Load empty session err = %v, want ErrInvalidArgument", err)
		}
		_, err = s.List(ctx, "")
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("List empty session err = %v, want ErrInvalidArgument", err)
		}
	})
}

func newTestCheckpoint(sessionID string, iter int, final bool, reason schema.StopReason) *Checkpoint {
	return &Checkpoint{
		SessionID: sessionID,
		AgentID:   "test-agent",
		Iteration: iter,
		Final:     final,
		StopReason: func() schema.StopReason {
			if final {
				return reason
			}
			return ""
		}(),
		Messages: []aimodel.Message{
			{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
			{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hi")},
		},
		SessionMsgCount: 0,
		Usage:           aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}
