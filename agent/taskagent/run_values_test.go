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
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// runValueKey is the shared key the scripted writer/reader tools below use.
const runValueKey = "test/run-values/handoff"

// runValueProbe records what the reader tool observed, guarded because
// parallel tool batches call the handlers from several goroutines.
type runValueProbe struct {
	mu sync.Mutex

	reads     []any
	found     []bool
	sessionID string
	setOK     []bool
}

func (p *runValueProbe) recordWrite(ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.setOK = append(p.setOK, ok)
}

func (p *runValueProbe) recordRead(ctx context.Context, v any, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.reads = append(p.reads, v)
	p.found = append(p.found, ok)
	p.sessionID = schema.SessionIDFromContext(ctx)
}

func (p *runValueProbe) snapshot() ([]any, []bool, []bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]any(nil), p.reads...),
		append([]bool(nil), p.found...),
		append([]bool(nil), p.setOK...),
		p.sessionID
}

// newRunValueRegistry registers a "write" tool that publishes value under the
// shared key and a "read" tool that reads it back — the two halves of the
// cross-tool handoff the run-value API exists for.
func newRunValueRegistry(probe *runValueProbe, value any) tool.ToolRegistry {
	r := tool.NewRegistry()

	_ = r.Register(
		schema.ToolDef{Name: "write"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			probe.recordWrite(schema.SetRunValue(ctx, runValueKey, value))
			return schema.TextResult("", "written"), nil
		},
	)
	_ = r.Register(
		schema.ToolDef{Name: "read"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			v, ok := schema.GetRunValue(ctx, runValueKey)
			probe.recordRead(ctx, v, ok)
			return schema.TextResult("", "read"), nil
		},
	)

	return r
}

// TestRunValues_SharedAcrossReactRounds_Sync is the core acceptance case: a
// tool in ReAct round 1 writes a key and a different tool in round 2 reads it
// back through the same Run.
func TestRunValues_SharedAcrossReactRounds_Sync(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "payload-sync")

	mock := newMock(
		toolCallResponse("tc-1", "write", `{}`),
		toolCallResponse("tc-2", "read", `{}`),
		stopResponse("done"),
	)

	a := New(agent.Config{ID: "rv-sync"}, WithCaller(mock), WithToolRegistry(reg))

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-sync",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reads, found, setOK, sessionID := probe.snapshot()

	if len(setOK) != 1 || !setOK[0] {
		t.Fatalf("SetRunValue in a TaskAgent run must report true, got %v", setOK)
	}
	if len(found) != 1 || !found[0] {
		t.Fatalf("round-2 tool did not see the round-1 value: found=%v", found)
	}
	if reads[0] != "payload-sync" {
		t.Fatalf("read value = %v, want %q", reads[0], "payload-sync")
	}
	if sessionID != "sess-rv-sync" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "sess-rv-sync")
	}
}

// TestRunValues_SharedAcrossReactRounds_Stream mirrors the sync case on the
// RunStream path, where the store must survive for the whole stream body.
func TestRunValues_SharedAcrossReactRounds_Stream(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "payload-stream")

	srv := sseStreamServer(t, [][]string{
		toolCallChunks("stc-1", "write", `{}`),
		toolCallChunks("stc-2", "read", `{}`),
		{textDeltaChunk("done"), stopChunk()},
	})
	defer srv.Close()

	client, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "test", BaseURL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("largemodel.NewOpenAIChatCallerFromConfig: %v", err)
	}

	a := New(agent.Config{ID: "rv-stream"}, WithCaller(client), WithToolRegistry(reg))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-stream",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	drainStream(t, stream)

	reads, found, _, _ := probe.snapshot()

	if len(found) != 1 || !found[0] {
		t.Fatalf("round-2 tool did not see the round-1 value: found=%v", found)
	}
	if reads[0] != "payload-stream" {
		t.Fatalf("read value = %v, want %q", reads[0], "payload-stream")
	}
}

// TestRunValues_IsolatedBetweenRuns pins the run boundary: two back-to-back
// runs on the same agent and the same SessionID must not share values.
func TestRunValues_IsolatedBetweenRuns(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "payload-first")

	mock := newMock(
		toolCallResponse("tc-1", "write", `{}`),
		stopResponse("first done"),
		toolCallResponse("tc-2", "read", `{}`),
		stopResponse("second done"),
	)

	a := New(agent.Config{ID: "rv-isolation"}, WithCaller(mock), WithToolRegistry(reg))

	req := func() *schema.RunRequest {
		return &schema.RunRequest{
			SessionID: "sess-rv-same",
			Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
		}
	}

	if _, err := a.Run(context.Background(), req()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := a.Run(context.Background(), req()); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	_, found, _, _ := probe.snapshot()

	if len(found) != 1 {
		t.Fatalf("read tool ran %d times, want 1", len(found))
	}
	if found[0] {
		t.Fatalf("the second run must not observe the first run's value")
	}
}

// TestRunValues_ParentContextShadowed guards the nested-run rule at the agent
// boundary: values preset on the caller's context stay invisible to the run.
func TestRunValues_ParentContextShadowed(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "unused")

	mock := newMock(
		toolCallResponse("tc-1", "read", `{}`),
		stopResponse("done"),
	)

	a := New(agent.Config{ID: "rv-parent"}, WithCaller(mock), WithToolRegistry(reg))

	parent := schema.WithRunValues(context.Background())
	schema.SetRunValue(parent, runValueKey, "from-parent")

	if _, err := a.Run(parent, &schema.RunRequest{
		SessionID: "sess-rv-parent",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	_, found, _, _ := probe.snapshot()

	if len(found) != 1 || found[0] {
		t.Fatalf("run must shadow the parent context store, found=%v", found)
	}
	if v, ok := schema.GetRunValue(parent, runValueKey); !ok || v != "from-parent" {
		t.Fatalf("parent store was disturbed: %v (ok=%v)", v, ok)
	}
}

// TestRunValues_EmptySessionID proves the store does not depend on a
// SessionID: an anonymous run can still hand values between its tools.
func TestRunValues_EmptySessionID(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "payload-anon")

	mock := newMock(
		toolCallResponse("tc-1", "write", `{}`),
		toolCallResponse("tc-2", "read", `{}`),
		stopResponse("done"),
	)

	a := New(agent.Config{ID: "rv-anon"}, WithCaller(mock), WithToolRegistry(reg))

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reads, found, setOK, sessionID := probe.snapshot()

	if len(setOK) != 1 || !setOK[0] {
		t.Fatalf("SetRunValue must work without a SessionID, got %v", setOK)
	}
	if len(found) != 1 || !found[0] || reads[0] != "payload-anon" {
		t.Fatalf("anonymous run lost its value: reads=%v found=%v", reads, found)
	}
	if sessionID != "" {
		t.Fatalf("SessionIDFromContext = %q, want empty", sessionID)
	}
}

// TestRunValues_ParallelBatchSharesStore drives a parallel tool batch so
// several handlers hit one store concurrently, then asserts a later round
// sees every write. It makes no claim about ordering inside the batch.
func TestRunValues_ParallelBatchSharesStore(t *testing.T) {
	const writers = 4

	var (
		mu   sync.Mutex
		seen map[string]any
	)

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "par_write"},
		func(ctx context.Context, _, args string) (schema.ToolResult, error) {
			if !schema.SetRunValue(ctx, args, args) {
				return schema.ErrorResult("", "no run values"), nil
			}
			return schema.TextResult("", "ok"), nil
		},
	)
	_ = reg.Register(
		schema.ToolDef{Name: "collect"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()

			seen = make(map[string]any, writers)

			for i := range writers {
				key := fmt.Sprintf(`{"i":%d}`, i)
				if v, ok := schema.GetRunValue(ctx, key); ok {
					seen[key] = v
				}
			}

			return schema.TextResult("", "ok"), nil
		},
	)

	calls := make([][3]string, 0, writers)
	for i := range writers {
		calls = append(calls, [3]string{fmt.Sprintf("tc-%d", i), "par_write", fmt.Sprintf(`{"i":%d}`, i)})
	}

	mock := newMock(
		batchToolCallResponse(calls...),
		toolCallResponse("tc-collect", "collect", `{}`),
		stopResponse("done"),
	)

	a := New(
		agent.Config{ID: "rv-parallel"},
		WithCaller(mock),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(writers),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-parallel",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(seen) != writers {
		t.Fatalf("collector saw %d of %d parallel writes: %v", len(seen), writers, seen)
	}
}

// TestRunValues_ResumeStartsEmpty verifies that a resumed run gets its own
// empty store — run values are never checkpointed, so nothing written before
// the interruption comes back.
func TestRunValues_ResumeStartsEmpty(t *testing.T) {
	probe := &runValueProbe{}
	reg := newRunValueRegistry(probe, "payload-before-crash")
	store := checkpoint.NewMapIterationStore()

	// First run writes the key, then the scripted caller runs out, which
	// aborts the run after a non-final checkpoint was persisted.
	a1 := New(
		agent.Config{ID: "rv-resume"},
		WithCaller(newMock(toolCallResponse("tc-1", "write", `{}`))),
		WithToolRegistry(reg),
		WithIterationStore(store),
	)

	if _, err := a1.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-resume",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err == nil {
		t.Fatal("first Run: want error from the exhausted caller, got nil")
	}

	if _, _, setOK, _ := probe.snapshot(); len(setOK) != 1 || !setOK[0] {
		t.Fatalf("first run failed to write the value: %v", setOK)
	}

	// The resumed run re-enters the same session and agent id from the
	// checkpoint, and its tool must still start from an empty store.
	a2 := New(
		agent.Config{ID: "rv-resume"},
		WithCaller(newMock(
			toolCallResponse("tc-2", "read", `{}`),
			stopResponse("done"),
		)),
		WithToolRegistry(reg),
		WithIterationStore(store),
	)

	if _, err := a2.Resume(context.Background(), "sess-rv-resume"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	_, found, _, _ := probe.snapshot()

	if len(found) != 1 {
		t.Fatalf("read tool ran %d times, want 1", len(found))
	}
	if found[0] {
		t.Fatalf("a resumed run must not observe values from the interrupted run")
	}
}

// TestRunValues_CancelledStreamLeavesNoResidue covers the cancellation arm of
// the run-boundary rule: a stream aborted after its tool wrote a value must
// leave nothing a later run can observe.
func TestRunValues_CancelledStreamLeavesNoResidue(t *testing.T) {
	probe := &runValueProbe{}
	written := make(chan struct{})

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "write"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			probe.recordWrite(schema.SetRunValue(ctx, runValueKey, "payload-cancelled"))
			close(written)

			return schema.TextResult("", "written"), nil
		},
	)
	_ = reg.Register(
		schema.ToolDef{Name: "read"},
		func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
			v, ok := schema.GetRunValue(ctx, runValueKey)
			probe.recordRead(ctx, v, ok)

			return schema.TextResult("", "read"), nil
		},
	)

	srv := sseStreamServer(t, [][]string{
		toolCallChunks("stc-1", "write", `{}`),
		{textDeltaChunk("never delivered"), stopChunk()},
	})
	defer srv.Close()

	client, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "test", BaseURL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("largemodel.NewOpenAIChatCallerFromConfig: %v", err)
	}

	streamAgent := New(agent.Config{ID: "rv-cancel"}, WithCaller(client), WithToolRegistry(reg))

	stream, err := streamAgent.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-cancel",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	// Abort the run once the value is known to be in the store.
	<-written
	_ = stream.Close()

	if _, _, setOK, _ := probe.snapshot(); len(setOK) != 1 || !setOK[0] {
		t.Fatalf("cancelled run failed to write the value first: %v", setOK)
	}

	// A fresh run on the same agent and session must still start empty.
	syncAgent := New(
		agent.Config{ID: "rv-cancel"},
		WithCaller(newMock(
			toolCallResponse("tc-2", "read", `{}`),
			stopResponse("done"),
		)),
		WithToolRegistry(reg),
	)

	if _, err := syncAgent.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv-cancel",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	}); err != nil {
		t.Fatalf("follow-up Run: %v", err)
	}

	_, found, _, _ := probe.snapshot()

	if len(found) != 1 {
		t.Fatalf("read tool ran %d times, want 1", len(found))
	}
	if found[0] {
		t.Fatalf("a cancelled run must leave no value for the next run")
	}
}

// drainStream consumes a RunStream to completion, failing the test on any
// non-EOF error.
func drainStream(t *testing.T, stream *schema.RunStream) {
	t.Helper()

	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			t.Fatalf("Recv: %v", err)
		}
	}
}
