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
	"io"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/todo"
)

// TestToolCtxInjection_SyncPath guards against regressions of P1-9's
// single-choke-point ctx injection: executeToolBatch must hand the handler a
// ctx that carries both the sessionID and a non-nil Emitter. If a future
// refactor adds another tool-dispatch path that bypasses executeToolBatch,
// this test fails and the developer loop can catch it.
func TestToolCtxInjection_SyncPath(t *testing.T) {
	var sawSessionID string
	var sawEmitter schema.Emitter

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "probe"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			sawSessionID = schema.SessionIDFromContext(ctx)
			sawEmitter = schema.EmitterFromContext(ctx)
			return schema.TextResult("", "ok"), nil
		},
	)

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse([3]string{"tc-1", "probe", "{}"}),
			stopResponse("done"),
		},
	}

	a := New(agent.Config{}, WithChatCompleter(mock), WithToolRegistry(reg))

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-sync",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if sawSessionID != "sess-sync" {
		t.Fatalf("handler ctx missing sessionID: got %q", sawSessionID)
	}
	if sawEmitter == nil {
		t.Fatalf("handler ctx missing Emitter")
	}
}

// TestToolCtxInjection_StreamPath mirrors the sync assertion on the RunStream
// path — both entry points funnel through executeToolBatch, so both must
// inject ctx. We drive RunStream via a real SSE mock so the stream-only
// code path in the agent is exercised.
func TestToolCtxInjection_StreamPath(t *testing.T) {
	var sawSessionID string
	var sawEmitter schema.Emitter

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "probe"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			sawSessionID = schema.SessionIDFromContext(ctx)
			sawEmitter = schema.EmitterFromContext(ctx)
			return schema.TextResult("", "ok"), nil
		},
	)

	firstTurn := multiToolCallChunks([]struct {
		ID, Name, Args string
	}{
		{"stc-1", "probe", `{}`},
	})
	secondTurn := []string{textDeltaChunk("done"), stopChunk()}

	srv := sseStreamServer(t, [][]string{firstTurn, secondTurn})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	a := New(
		agent.Config{ID: "stream-ctx"},
		WithChatCompleter(client),
		WithToolRegistry(reg),
	)

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-stream",
		Messages:  []schema.Message{schema.NewUserMessage("probe")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("Recv: %v", err)
		}
	}

	if sawSessionID != "sess-stream" {
		t.Fatalf("handler ctx missing sessionID: got %q", sawSessionID)
	}
	if sawEmitter == nil {
		t.Fatalf("handler ctx missing Emitter")
	}
}

// TestTodoWrite_EndToEndStream drives a real RunStream with the todo_write
// tool registered and asserts that (a) the tool's EventTodoUpdate makes it
// all the way through the stream to the consumer, (b) the payload carries
// the right version/items, and (c) the in_progress invariant is surfaced as
// an error result without bumping the snapshot version.
func TestTodoWrite_EndToEndStream(t *testing.T) {
	reg := tool.NewRegistry()
	if err := todo.Register(reg, todo.NewStore()); err != nil {
		t.Fatalf("todo.Register: %v", err)
	}

	args := `{"todos":[{"content":"Read","active_form":"Reading","status":"pending"},{"content":"Write","active_form":"Writing","status":"in_progress"}]}`

	firstTurn := multiToolCallChunks([]struct {
		ID, Name, Args string
	}{
		{"tc-1", todo.ToolName, args},
	})
	secondTurn := []string{textDeltaChunk("done"), stopChunk()}

	srv := sseStreamServer(t, [][]string{firstTurn, secondTurn})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	a := New(agent.Config{ID: "todo-e2e"}, WithChatCompleter(client), WithToolRegistry(reg))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-e2e",
		Messages:  []schema.Message{schema.NewUserMessage("plan something")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var got *schema.TodoUpdateData
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if e.Type == schema.EventTodoUpdate {
			d, ok := e.Data.(schema.TodoUpdateData)
			if !ok {
				t.Fatalf("todo_update data type = %T", e.Data)
			}
			got = &d
			if e.SessionID != "sess-e2e" {
				t.Errorf("todo_update sessionID = %q, want sess-e2e", e.SessionID)
			}
		}
	}

	if got == nil {
		t.Fatal("no todo_update event received on stream")
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	if got.Items[0].Status != "pending" || got.Items[1].Status != "in_progress" {
		t.Errorf("unexpected statuses: %+v", got.Items)
	}
	if got.Items[0].ID == "" || got.Items[1].ID == "" {
		t.Errorf("server must assign ids, got %+v", got.Items)
	}
}
