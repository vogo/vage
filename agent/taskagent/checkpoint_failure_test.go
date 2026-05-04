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

package taskagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/schema"
)

// failingIterationStore is an IterationStore whose Save always returns
// the configured error. List/Load/Delete delegate to the embedded
// MapIterationStore so the rest of the contract still works.
type failingIterationStore struct {
	*checkpoint.MapIterationStore
	saveErr  error
	saveHits atomic.Int32
}

func newFailingIterationStore(saveErr error) *failingIterationStore {
	return &failingIterationStore{
		MapIterationStore: checkpoint.NewMapIterationStore(),
		saveErr:           saveErr,
	}
}

func (s *failingIterationStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.saveHits.Add(1)
	return s.saveErr
}

// TestSaveCheckpointFailure_InvokesCallback drives a Run that writes
// 3 checkpoints (2 iter + 1 final) against a store that fails every
// Save. The callback must fire exactly 3 times; Run must still
// complete normally because checkpointing is best-effort.
func TestSaveCheckpointFailure_InvokesCallback(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "echo", `{"v":"a"}`),
			toolCallResponse("tc-2", "echo", `{"v":"b"}`),
			stopResponse("final"),
		},
	}
	saveErr := errors.New("disk full")
	store := newFailingIterationStore(saveErr)
	registry := newEchoRegistry()

	var (
		cbHits     atomic.Int32
		seenSidPtr atomic.Pointer[string]
		seenErrPtr atomic.Pointer[error]
	)

	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(mock),
		WithIterationStore(store),
		WithToolRegistry(registry),
		WithCheckpointFailureCallback(func(_ context.Context, sid string, err error) {
			cbHits.Add(1)
			s := sid
			e := err
			seenSidPtr.Store(&s)
			seenErrPtr.Store(&e)
		}),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-cb",
		Messages:  []schema.Message{schema.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete (failures should not abort)", resp.StopReason)
	}

	wantHits := int32(3) // 2 iter + 1 final
	if got := cbHits.Load(); got != wantHits {
		t.Errorf("callback hits = %d, want %d", got, wantHits)
	}
	if got := store.saveHits.Load(); got != wantHits {
		t.Errorf("Save hits = %d, want %d", got, wantHits)
	}

	if seen := seenSidPtr.Load(); seen == nil || *seen != "sess-cb" {
		t.Errorf("callback sid = %v, want 'sess-cb'", seen)
	}
	if seen := seenErrPtr.Load(); seen == nil || !errors.Is(*seen, saveErr) {
		t.Errorf("callback err = %v, want chain to include %v", seen, saveErr)
	}
}

// TestSaveCheckpointFailure_NilCallback_NoPanic exercises the zero-
// value path: when no callback is configured, save failures must
// continue to fall through to slog.Warn alone without nil-deref.
func TestSaveCheckpointFailure_NilCallback_NoPanic(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("done")},
	}
	store := newFailingIterationStore(errors.New("disk full"))

	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(mock),
		WithIterationStore(store),
		// no WithCheckpointFailureCallback
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-nilcb",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := store.saveHits.Load(); got != 1 {
		t.Errorf("Save hits = %d, want 1 (final only)", got)
	}
}

// TestSaveCheckpointFailure_OnSuccess_NoCallbackInvoke confirms the
// callback fires only on failure — successful saves stay silent so
// the SessionMetricsHook's CheckpointSaveFailures counter remains a
// faithful "failure count" rather than a generic "save attempt".
func TestSaveCheckpointFailure_OnSuccess_NoCallbackInvoke(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("done")},
	}
	store := checkpoint.NewMapIterationStore()

	var cbHits atomic.Int32
	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(mock),
		WithIterationStore(store),
		WithCheckpointFailureCallback(func(context.Context, string, error) {
			cbHits.Add(1)
		}),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-success",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := cbHits.Load(); got != 0 {
		t.Errorf("callback hits = %d, want 0 on successful Run", got)
	}
}
