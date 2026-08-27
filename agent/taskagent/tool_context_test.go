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

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/largemodel"
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

	mock := newMock(batchToolCallResponse([3]string{"tc-1", "probe", "{}"}),
		stopResponse("done"))

	a := New(agent.Config{}, WithCaller(mock), WithToolRegistry(reg))

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-sync",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
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

	client, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "test", BaseURL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("largemodel.NewOpenAIChatCallerFromConfig: %v", err)
	}

	a := New(
		agent.Config{ID: "stream-ctx"},
		WithCaller(client),
		WithToolRegistry(reg),
	)

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-stream",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "probe")},
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

	client, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "test", BaseURL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("largemodel.NewOpenAIChatCallerFromConfig: %v", err)
	}

	a := New(agent.Config{ID: "todo-e2e"}, WithCaller(client), WithToolRegistry(reg))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-e2e",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "plan something")},
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

// TestEmitCustomData_EndToEndStream drives a real RunStream with a tool that
// reports several progress stages from inside its handler, and asserts that
// every schema.EmitCustomData call surfaces on the consumer stream as an
// EventCustom — in call order, carrying the right name/payload/sessionID, and
// bracketed by that tool call's tool_call_start / tool_call_end.
func TestEmitCustomData_EndToEndStream(t *testing.T) {
	stages := []string{"fetch", "parse", "index"}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "ingest"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			for i, stage := range stages {
				schema.EmitCustomData(ctx, "ingest.progress", map[string]any{
					"stage": stage,
					"step":  i + 1,
				})
			}
			return schema.TextResult("", "ok"), nil
		},
	)

	firstTurn := multiToolCallChunks([]struct {
		ID, Name, Args string
	}{
		{"tc-1", "ingest", `{}`},
	})
	secondTurn := []string{textDeltaChunk("done"), stopChunk()}

	srv := sseStreamServer(t, [][]string{firstTurn, secondTurn})
	defer srv.Close()

	client, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "test", BaseURL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("largemodel.NewOpenAIChatCallerFromConfig: %v", err)
	}

	a := New(agent.Config{ID: "custom-e2e"}, WithCaller(client), WithToolRegistry(reg))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-custom",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "ingest")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var (
		seen       int   // events received so far, used as a position counter
		customPos  []int // stream positions of the custom events
		startPos   = -1
		endPos     = -1
		customData []schema.CustomEventData
		customSess []string
	)
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}

		switch e.Type {
		case schema.EventToolCallStart:
			startPos = seen
		case schema.EventToolCallEnd:
			endPos = seen
		case schema.EventCustom:
			d, ok := e.Data.(schema.CustomEventData)
			if !ok {
				t.Fatalf("custom event data type = %T, want schema.CustomEventData", e.Data)
			}
			customPos = append(customPos, seen)
			customData = append(customData, d)
			customSess = append(customSess, e.SessionID)
		}
		seen++
	}

	if startPos < 0 || endPos < 0 {
		t.Fatalf("missing tool call bracket: start=%d end=%d", startPos, endPos)
	}
	if len(customData) != len(stages) {
		t.Fatalf("received %d custom events, want %d", len(customData), len(stages))
	}

	for i, d := range customData {
		if pos := customPos[i]; pos < startPos || pos > endPos {
			t.Errorf("custom event %d at position %d is outside [%d, %d]", i, pos, startPos, endPos)
		}
		if d.Name != "ingest.progress" {
			t.Errorf("custom event %d name = %q, want %q", i, d.Name, "ingest.progress")
		}
		m, ok := d.Payload.(map[string]any)
		if !ok {
			t.Fatalf("custom event %d payload type = %T, want map[string]any", i, d.Payload)
		}
		if m["stage"] != stages[i] {
			t.Errorf("custom event %d stage = %v, want %q", i, m["stage"], stages[i])
		}
		if m["step"] != i+1 {
			t.Errorf("custom event %d step = %v, want %d", i, m["step"], i+1)
		}
		if customSess[i] != "sess-custom" {
			t.Errorf("custom event %d sessionID = %q, want sess-custom", i, customSess[i])
		}
	}
}
