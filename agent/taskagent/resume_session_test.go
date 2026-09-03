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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// loadOverrideStore returns a fixed checkpoint from Load so tests can
// make Checkpoint.SessionID disagree with the Resume query parameter.
type loadOverrideStore struct {
	*checkpoint.MapIterationStore
	override *checkpoint.Checkpoint
}

func (s *loadOverrideStore) Load(ctx context.Context, sessionID, id string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.override == nil {
		return s.MapIterationStore.Load(ctx, sessionID, id)
	}
	cp := *s.override
	cp.Messages = append([]schema.Message(nil), s.override.Messages...)
	return &cp, nil
}

func TestResume_CheckpointSessionIDWins(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cp := &checkpoint.Checkpoint{
		SessionID: "sess-checkpoint",
		AgentID:   "resume-ns",
		Iteration: 0,
		Final:     false,
		Messages: []schema.Message{
			schema.NewUserMessage(testProtocol, "from-checkpoint"),
		},
		SessionMsgCount: 1,
	}
	iterStore := &loadOverrideStore{
		MapIterationStore: checkpoint.NewMapIterationStore(),
		override:          cp,
	}

	shared := memory.NewMapStore()
	session := memory.NewSessionMemoryWithStore(shared, "resume-ns", "unused")
	memMgr := memory.NewManager(memory.WithSession(session))

	var (
		mu      sync.Mutex
		sessIDs []string
		endSess string
		endMsg  string
	)
	hooks := hook.NewManager()
	hooks.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		mu.Lock()
		defer mu.Unlock()
		sessIDs = append(sessIDs, e.SessionID)
		if e.Type == schema.EventAgentEnd {
			endSess = e.SessionID
			if data, ok := e.Data.(schema.AgentEndData); ok {
				endMsg = data.Message
			}
		}
		return nil
	}))

	mock := newMock(stopResponse("resumed-ok"))
	a := New(
		agent.Config{ID: "resume-ns"},
		WithCaller(mock),
		WithIterationStore(iterStore),
		WithMemory(memMgr),
		WithHookManager(hooks),
	)

	resp, err := a.Resume(context.Background(), "sess-query")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resp.SessionID != "sess-checkpoint" {
		t.Fatalf("resp.SessionID = %q, want sess-checkpoint", resp.SessionID)
	}
	if endSess != "sess-checkpoint" {
		t.Fatalf("AgentEnd session = %q, want sess-checkpoint", endSess)
	}
	if endMsg != "resumed-ok" {
		t.Fatalf("AgentEnd message = %q, want resumed-ok", endMsg)
	}
	for _, sid := range sessIDs {
		if sid == "sess-query" {
			t.Fatal("resume events used the query session id")
		}
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "sess-query") || !strings.Contains(logs, "sess-checkpoint") {
		t.Fatalf("warn did not record both session ids; logs=%q", logs)
	}

	ctx := context.Background()
	gotCP, err := memMgr.ForSession("resume-ns", "sess-checkpoint").Session().List(ctx, "msg:")
	if err != nil {
		t.Fatalf("checkpoint session List: %v", err)
	}
	if len(gotCP) == 0 {
		t.Fatal("resume wrote nothing into the checkpoint session")
	}
	gotQuery, err := memMgr.ForSession("resume-ns", "sess-query").Session().List(ctx, "msg:")
	if err != nil {
		t.Fatalf("query session List: %v", err)
	}
	if len(gotQuery) != 0 {
		t.Fatalf("query session received %d writes, want 0", len(gotQuery))
	}
}
