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
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// multiToolCallChunks produces an SSE chunk slice that represents one
// assistant message carrying multiple tool_calls, each at its own index.
// The chunks simulate what an OpenAI-compatible streaming response looks
// like when the LLM issues several parallel tool calls in a single turn.
func multiToolCallChunks(calls []struct {
	ID, Name, Args string
},
) []string {
	chunks := make([]string, 0, len(calls)*2+1)
	for i, c := range calls {
		// Header chunk — establishes index, id, name, type. Arguments are
		// sent in a follow-up chunk so we exercise the stream accumulator.
		chunks = append(chunks,
			fmt.Sprintf(
				`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]},"finish_reason":null}]}`,
				i, mustMarshal(c.ID), mustMarshal(c.Name)))
		// Arguments chunk.
		chunks = append(chunks,
			fmt.Sprintf(
				`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"function":{"arguments":%s}}]},"finish_reason":null}]}`,
				i, mustMarshal(c.Args)))
	}
	// Final chunk — finish_reason terminates the assistant message.
	chunks = append(chunks,
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	return chunks
}

// TestParallelToolCalls_StreamOrdering verifies AC-2.1 / AC-2.2 for the
// RunStream code path: even when the underlying tool calls finish out of
// order, the stream emits Start events in ToolCalls[i] order up-front,
// then End and Result events in the same order after every worker has
// completed. This guards the streaming branch of executeToolBatch, which
// is shared with Run but uses a different event sink (the stream's
// send() function instead of the hook-only dispatch).
func TestParallelToolCalls_StreamOrdering(t *testing.T) {
	// Assistant returns three concurrent tool calls in the first streaming
	// turn; after results come back, the second turn emits plain text and
	// stops the ReAct loop.
	firstTurn := multiToolCallChunks([]struct {
		ID, Name, Args string
	}{
		{"stc-1", "wait", `{"ms":90}`}, // slowest
		{"stc-2", "wait", `{"ms":10}`}, // fastest
		{"stc-3", "wait", `{"ms":45}`}, // mid
	})
	secondTurn := []string{textDeltaChunk("done"), stopChunk()}

	srv := sseStreamServer(t, [][]string{firstTurn, secondTurn})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "wait"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			ms := 0
			_, _ = fmt.Sscanf(args, `{"ms":%d}`, &ms)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return schema.TextResult("", fmt.Sprintf("slept %dms", ms)), nil
		},
	)

	a := New(
		agent.Config{ID: "stream-parallel"},
		WithChatCompleter(client),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(4),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("three waits")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	// Collect the tool-phase events in the order they arrive on the stream.
	type pair struct {
		typ string
		id  string
	}
	var pairs []pair
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		switch e.Type {
		case schema.EventToolCallStart:
			pairs = append(pairs, pair{e.Type, e.Data.(schema.ToolCallStartData).ToolCallID})
		case schema.EventToolCallEnd:
			pairs = append(pairs, pair{e.Type, e.Data.(schema.ToolCallEndData).ToolCallID})
		case schema.EventToolResult:
			pairs = append(pairs, pair{e.Type, e.Data.(schema.ToolResultData).ToolCallID})
		}
	}

	wantIDs := []string{"stc-1", "stc-2", "stc-3"}

	// Expect 3 Start + 3 End + 3 Result = 9 tool-phase events.
	if len(pairs) != 9 {
		t.Fatalf("tool-phase events = %d, want 9 (3 Start + 3 End + 3 Result). Events: %+v", len(pairs), pairs)
	}

	// First 3 must be Start in ToolCalls order.
	for i := range 3 {
		if pairs[i].typ != schema.EventToolCallStart || pairs[i].id != wantIDs[i] {
			t.Errorf("event %d = (%s, %s), want (Start, %s)", i, pairs[i].typ, pairs[i].id, wantIDs[i])
		}
	}

	// Remaining 6 events are End followed by Result for each id, in order.
	// Order is: End_1, Result_1, End_2, Result_2, End_3, Result_3.
	for i := range 3 {
		endIdx := 3 + i*2
		resIdx := endIdx + 1
		if pairs[endIdx].typ != schema.EventToolCallEnd || pairs[endIdx].id != wantIDs[i] {
			t.Errorf("event %d = (%s, %s), want (End, %s)", endIdx, pairs[endIdx].typ, pairs[endIdx].id, wantIDs[i])
		}
		if pairs[resIdx].typ != schema.EventToolResult || pairs[resIdx].id != wantIDs[i] {
			t.Errorf("event %d = (%s, %s), want (Result, %s)", resIdx, pairs[resIdx].typ, pairs[resIdx].id, wantIDs[i])
		}
	}
}
