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

package schema

import (
	"context"
	"strings"
	"testing"
)

func TestEventAccumulator_FoldsFullRun(t *testing.T) {
	var acc EventAccumulator

	for _, e := range []Event{
		NewEvent(EventAgentStart, "a", "sess-1", AgentStartData{}),
		NewEvent(EventIterationStart, "a", "sess-1", IterationStartData{Iteration: 0}),
		NewEvent(EventTextDelta, "a", "sess-1", TextDeltaData{Delta: "上海"}),
		NewEvent(EventToolCallStart, "a", "sess-1", ToolCallStartData{
			ToolCallID: "c1", ToolName: "weather", Arguments: `{"city":"上海"}`,
		}),
		NewEvent(EventToolResult, "a", "sess-1", ToolResultData{
			ToolCallID: "c1", ToolName: "weather", Result: TextResult("c1", "晴"),
		}),
		NewEvent(EventIterationStart, "a", "sess-1", IterationStartData{Iteration: 1}),
		NewEvent(EventTextDelta, "a", "sess-1", TextDeltaData{Delta: "今天晴"}),
		NewEvent(EventLLMCallEnd, "a", "sess-1", LLMCallEndData{
			PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
		}),
		NewEvent(EventLLMCallEnd, "a", "sess-1", LLMCallEndData{
			PromptTokens: 20, CompletionTokens: 6, TotalTokens: 26,
			CacheReadTokens: 8, CacheWriteTokens: 5, ReasoningTokens: 3,
		}),
		NewEvent(EventAgentEnd, "a", "sess-1", AgentEndData{
			Duration: 1234, StopReason: StopReasonComplete, Message: "上海今天晴",
		}),
	} {
		acc.Add(e)
	}

	if got, want := acc.Text(), "上海今天晴"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
	if got := acc.Iterations(); got != 2 {
		t.Errorf("Iterations() = %d, want 2", got)
	}
	if got := acc.SessionID(); got != "sess-1" {
		t.Errorf("SessionID() = %q, want %q", got, "sess-1")
	}
	if got := acc.StopReason(); got != StopReasonComplete {
		t.Errorf("StopReason() = %q, want %q", got, StopReasonComplete)
	}
	if got := acc.Duration(); got != 1234 {
		t.Errorf("Duration() = %d, want 1234", got)
	}
	if got := acc.Err(); got != nil {
		t.Errorf("Err() = %v, want nil", got)
	}

	calls := acc.ToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "weather" {
		t.Fatalf("ToolCalls() = %+v, want one weather call", calls)
	}

	results := acc.ToolResults()
	if len(results) != 1 || results[0].Result.Text() != "晴" {
		t.Fatalf("ToolResults() = %+v, want one result carrying 晴", results)
	}

	usage := acc.Usage()
	if usage == nil {
		t.Fatal("Usage() = nil, want the summed usage of both LLM calls")
	}
	if usage.TotalTokens != 40 || usage.PromptTokens != 30 || usage.CacheReadTokens != 8 {
		t.Errorf("Usage() = %+v, want prompt=30 total=40 cacheRead=8", usage)
	}

	// Every dimension Usage.Add sums must survive the event layer, or folding
	// events yields a quieter bill than Run reports.
	if usage.CacheWriteTokens != 5 || usage.ReasoningTokens != 3 {
		t.Errorf("Usage() = %+v, want cacheWrite=5 reasoning=3", usage)
	}
}

func TestEventAccumulator_UsageNilWithoutLLMCallEnd(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventTextDelta, "a", "s", TextDeltaData{Delta: "hi"}))
	acc.Add(NewEvent(EventAgentEnd, "a", "s", AgentEndData{StopReason: StopReasonComplete}))

	// A run without the metrics middleware must report "unknown", not "zero".
	if got := acc.Usage(); got != nil {
		t.Errorf("Usage() = %+v, want nil when no LLMCallEnd event arrived", got)
	}
}

func TestEventAccumulator_CollectsErrorEvents(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventError, "a", "s", ErrorData{Message: "tool exploded"}))
	acc.Add(NewEvent(EventError, "a", "s", ErrorData{Message: "and again"}))

	err := acc.Err()
	if err == nil {
		t.Fatal("Err() = nil, want the joined error events")
	}
	for _, want := range []string{"tool exploded", "and again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Err() = %q, want it to mention %q", err.Error(), want)
		}
	}
}

func TestEventAccumulator_ResponseRebuildsAssistantMessage(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventTextDelta, "a", "sess-9", TextDeltaData{Delta: "answer"}))
	acc.Add(NewEvent(EventAgentEnd, "a", "sess-9", AgentEndData{
		Duration: 42, StopReason: StopReasonMaxIterations, Message: "answer",
	}))

	resp := acc.Response(ProtocolOpenAIChat)
	if len(resp.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(resp.Messages))
	}
	if got := resp.Messages[0].Text(); got != "answer" {
		t.Errorf("Messages[0].Text() = %q, want %q", got, "answer")
	}
	if got := resp.Messages[0].Role(); got != RoleAssistant {
		t.Errorf("Messages[0].Role() = %q, want %q", got, RoleAssistant)
	}
	if resp.SessionID != "sess-9" || resp.Duration != 42 || resp.StopReason != StopReasonMaxIterations {
		t.Errorf("Response() = %+v, want sess-9 / 42ms / max_iterations", resp)
	}
	if resp.Interrupt != nil {
		t.Error("Interrupt must stay nil: the event stream does not carry it")
	}
}

func TestEventAccumulator_ResponseWithoutTextHasNoMessages(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventAgentEnd, "a", "s", AgentEndData{StopReason: StopReasonComplete}))

	if got := acc.Response(ProtocolOpenAIChat).Messages; len(got) != 0 {
		t.Errorf("Messages = %+v, want none when the run produced no text", got)
	}
}

// A stream shaped like agent.RunToStream's — AgentStart then AgentEnd, no
// deltas at all — is how every non-streaming agent (RouterAgent,
// WorkflowAgent, any custom Agent) reaches a stream consumer. Reading only
// deltas would report those runs as empty.
func TestEventAccumulator_ResponseFromDeltalessStream(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventAgentStart, "a", "sess-r", AgentStartData{}))
	acc.Add(NewEvent(EventAgentEnd, "a", "sess-r", AgentEndData{
		Duration: 7, Message: "routed answer",
	}))

	if got, _ := acc.Message(); got != "routed answer" {
		t.Errorf("Message() = %q, want %q", got, "routed answer")
	}

	resp := acc.Response(ProtocolOpenAIChat)
	if len(resp.Messages) != 1 || resp.Messages[0].Text() != "routed answer" {
		t.Fatalf("Messages = %+v, want the terminal message", resp.Messages)
	}
}

// Output guards and Agent middleware rewrite the answer after the deltas are
// already on the wire, and the framework does not replay them — AgentEnd is
// the single source of truth (agent-core AC-11). Folding the deltas instead
// would hand the caller pre-guard text.
func TestEventAccumulator_TerminalMessageWinsOverDeltas(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventTextDelta, "a", "s", TextDeltaData{Delta: "我的邮箱是 user@"}))
	acc.Add(NewEvent(EventTextDelta, "a", "s", TextDeltaData{Delta: "example.com"}))
	acc.Add(NewEvent(EventAgentEnd, "a", "s", AgentEndData{
		Message: "我的邮箱是 [REDACTED]", StopReason: StopReasonComplete,
	}))

	// Text still reports what the stream actually showed.
	if got := acc.Text(); got != "我的邮箱是 user@example.com" {
		t.Errorf("Text() = %q, want the live delta trace", got)
	}

	resp := acc.Response(ProtocolOpenAIChat)
	if len(resp.Messages) != 1 || resp.Messages[0].Text() != "我的邮箱是 [REDACTED]" {
		t.Fatalf("Messages = %+v, want the post-guard terminal message", resp.Messages)
	}
}

// "Finished and produced nothing" is not the same as "was cut off after
// producing something": an arriving AgentEnd is authoritative even when empty.
func TestEventAccumulator_EmptyTerminalMessageIsAuthoritative(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventTextDelta, "a", "s", TextDeltaData{Delta: "draft"}))
	acc.Add(NewEvent(EventAgentEnd, "a", "s", AgentEndData{StopReason: StopReasonComplete}))

	msg, ended := acc.Message()
	if msg != "" || !ended {
		t.Errorf("Message() = (%q, %v), want (\"\", true)", msg, ended)
	}

	if got := acc.Response(ProtocolOpenAIChat).Messages; len(got) != 0 {
		t.Errorf("Messages = %+v, want none: the run reported an empty answer", got)
	}
}

// Without an AgentEnd there is no authoritative answer, so the deltas are the
// best available account of an interrupted run.
func TestEventAccumulator_FallsBackToDeltasWithoutAgentEnd(t *testing.T) {
	var acc EventAccumulator

	acc.Add(NewEvent(EventTextDelta, "a", "s", TextDeltaData{Delta: "partial"}))

	if msg, ended := acc.Message(); msg != "" || ended {
		t.Errorf("Message() = (%q, %v), want (\"\", false)", msg, ended)
	}

	resp := acc.Response(ProtocolOpenAIChat)
	if len(resp.Messages) != 1 || resp.Messages[0].Text() != "partial" {
		t.Fatalf("Messages = %+v, want the accumulated deltas", resp.Messages)
	}
	if resp.StopReason != "" {
		t.Errorf("StopReason = %q, want empty on a stream that never ended", resp.StopReason)
	}
}

func TestEventAccumulator_ZeroValueAndUnknownEvents(t *testing.T) {
	var acc EventAccumulator

	// Unrecognised payloads must not panic — new event types keep arriving.
	acc.Add(NewEvent("some.future.event", "a", "s", nil))
	acc.Add(NewEvent(EventGuardCheck, "a", "s", nil))

	if got := acc.Text(); got != "" {
		t.Errorf("Text() = %q, want empty", got)
	}
	if got := acc.Response(ProtocolOpenAIChat); got == nil {
		t.Error("Response() = nil, want an empty response")
	}
}

// The accumulator is the drain callback's natural partner: ForEach feeds it.
func TestEventAccumulator_WithForEach(t *testing.T) {
	rs := NewRunStream(context.Background(), 8, func(_ context.Context, send func(Event) error) error {
		for _, part := range []string{"a", "b", "c"} {
			if err := send(NewEvent(EventTextDelta, "ag", "s7", TextDeltaData{Delta: part})); err != nil {
				return err
			}
		}
		return send(NewEvent(EventAgentEnd, "ag", "s7", AgentEndData{
			Message: "abc", StopReason: StopReasonComplete,
		}))
	})

	var acc EventAccumulator
	if err := rs.ForEach(func(e Event) error {
		acc.Add(e)
		return nil
	}); err != nil {
		t.Fatalf("ForEach error = %v", err)
	}

	if got := acc.Text(); got != "abc" {
		t.Errorf("Text() = %q, want %q", got, "abc")
	}
	if got := acc.StopReason(); got != StopReasonComplete {
		t.Errorf("StopReason() = %q, want %q", got, StopReasonComplete)
	}
}
