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

package session

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// TestMetricsHook_Filter_LimitedToCounters guards the contract that
// the hook only subscribes to events that move a counter — anything
// broader would waste a goroutine wake under the async manager.
func TestMetricsHook_Filter_LimitedToCounters(t *testing.T) {
	h := NewSessionMetricsHook(NewMapMetricsStore())
	got := h.Filter()
	sort.Strings(got)

	want := []string{
		schema.EventAgentEnd,
		schema.EventContextEdited,
		schema.EventLLMCallEnd,
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("filter = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filter[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMetricsHook_LLMCallEnd_AccumulatesTokens checks that a single
// LLM-call event drives both prompt and completion counters and the
// auto-derived TotalTokens line.
func TestMetricsHook_LLMCallEnd_AccumulatesTokens(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	event := schema.NewEvent(schema.EventLLMCallEnd, "agent-1", "sid-llm",
		schema.LLMCallEndData{
			Model:            "test-model",
			PromptTokens:     20,
			CompletionTokens: 5,
			TotalTokens:      25,
			Duration:         100,
		})

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	got, err := store.Get(context.Background(), "sid-llm")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PromptTokens != 20 || got.CompletionTokens != 5 || got.TotalTokens != 25 {
		t.Errorf("counters = %+v", got)
	}
	if got.CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0 (no pricing configured)", got.CostUSD)
	}
}

// TestMetricsHook_LLMCallEnd_AppliesPricing wires a pricing function
// and verifies the dollar amount is computed from per-1k tokens. Two
// distinct calls accumulate so we confirm the fold is additive.
func TestMetricsHook_LLMCallEnd_AppliesPricing(t *testing.T) {
	store := NewMapMetricsStore()
	pricing := func(model string) (float64, float64, bool) {
		if model != "claude-test" {
			return 0, 0, false
		}
		// $3 / 1k prompt, $15 / 1k completion (parity with Claude Sonnet pricing).
		return 3.0, 15.0, true
	}
	h := NewSessionMetricsHook(store, WithMetricsPricing(pricing))

	first := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid-cost",
		schema.LLMCallEndData{Model: "claude-test", PromptTokens: 1000, CompletionTokens: 100})
	second := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid-cost",
		schema.LLMCallEndData{Model: "claude-test", PromptTokens: 500, CompletionTokens: 50})

	for _, e := range []schema.Event{first, second} {
		if err := h.OnEvent(context.Background(), e); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
	}

	got, _ := store.Get(context.Background(), "sid-cost")

	// First: 1000*3/1000 + 100*15/1000 = 3.00 + 1.50 = 4.50
	// Second: 500*3/1000 + 50*15/1000 = 1.50 + 0.75 = 2.25
	// Total: 6.75
	want := 6.75
	if got.CostUSD < want-0.001 || got.CostUSD > want+0.001 {
		t.Errorf("CostUSD = %f, want %f", got.CostUSD, want)
	}
}

// TestMetricsHook_LLMCallEnd_UnknownModel keeps cost at zero when the
// pricing table has no entry for the model. The counters still tick
// so observability stays useful even with sparse pricing data.
func TestMetricsHook_LLMCallEnd_UnknownModel(t *testing.T) {
	store := NewMapMetricsStore()
	pricing := func(_ string) (float64, float64, bool) { return 0, 0, false } // always miss
	h := NewSessionMetricsHook(store, WithMetricsPricing(pricing))

	event := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid-unk",
		schema.LLMCallEndData{Model: "ghost-model", PromptTokens: 100, CompletionTokens: 10})

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	got, _ := store.Get(context.Background(), "sid-unk")
	if got.CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0 for unknown model", got.CostUSD)
	}
	if got.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", got.PromptTokens)
	}
}

// TestMetricsHook_AgentEnd_AccumulatesActiveSeconds verifies the
// duration→seconds conversion and the across-Run sum semantics.
func TestMetricsHook_AgentEnd_AccumulatesActiveSeconds(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	for _, ms := range []int64{1500, 2300, 700} {
		ev := schema.NewEvent(schema.EventAgentEnd, "agent", "sid-time",
			schema.AgentEndData{Duration: ms})
		if err := h.OnEvent(context.Background(), ev); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
	}

	got, _ := store.Get(context.Background(), "sid-time")
	// 1500/1000 + 2300/1000 + 700/1000 = 1 + 2 + 0 = 3 (integer truncation)
	if got.ActiveSeconds != 3 {
		t.Errorf("ActiveSeconds = %d, want 3", got.ActiveSeconds)
	}
}

// TestMetricsHook_AgentEnd_ZeroDurationIgnored covers the edge where
// an immediately-finished agent emits Duration=0. The hook must not
// take a write lock for a no-op contribution.
func TestMetricsHook_AgentEnd_ZeroDurationIgnored(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	ev := schema.NewEvent(schema.EventAgentEnd, "agent", "sid-zero",
		schema.AgentEndData{Duration: 0})
	if err := h.OnEvent(context.Background(), ev); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	if _, err := store.Get(context.Background(), "sid-zero"); err == nil {
		t.Error("Update should not have been called — record exists")
	}
}

// TestMetricsHook_ContextEdited_StrategyDiscrimination is the gating
// test for P0-4 wiring: only the elide-to-artifact strategy should
// also bump ElidedArtifacts; keep_last_k events bump only the generic
// ContextEdits counter.
func TestMetricsHook_ContextEdited_StrategyDiscrimination(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	keepLastK := schema.NewEvent(schema.EventContextEdited, "agent", "sid-edits",
		schema.ContextEditedData{Edited: 2, Strategy: "keep_last_k"})
	elide := schema.NewEvent(schema.EventContextEdited, "agent", "sid-edits",
		schema.ContextEditedData{Edited: 1, Strategy: ContextEditStrategyElideArtifact})

	for _, e := range []schema.Event{keepLastK, keepLastK, elide} {
		if err := h.OnEvent(context.Background(), e); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
	}

	got, _ := store.Get(context.Background(), "sid-edits")
	if got.ContextEdits != 3 {
		t.Errorf("ContextEdits = %d, want 3", got.ContextEdits)
	}
	if got.ElidedArtifacts != 1 {
		t.Errorf("ElidedArtifacts = %d, want 1", got.ElidedArtifacts)
	}
}

// TestMetricsHook_RecordResume bumps ResumeCount and times. Calling
// twice in sequence must produce 2 — proves the counter is a real
// session-scoped accumulator, not a one-shot.
func TestMetricsHook_RecordResume(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	for range 2 {
		if err := h.RecordResume(context.Background(), "sid-resume"); err != nil {
			t.Fatalf("RecordResume: %v", err)
		}
	}

	got, _ := store.Get(context.Background(), "sid-resume")
	if got.ResumeCount != 2 {
		t.Errorf("ResumeCount = %d, want 2", got.ResumeCount)
	}
}

// TestMetricsHook_RecordCheckpointFailure feeds the counter that
// taskagent's CheckpointFailureCallback drives in production. The
// callback must be safe to invoke before any successful writes (so
// the session record may need to be bootstrapped here).
func TestMetricsHook_RecordCheckpointFailure(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	for range 3 {
		if err := h.RecordCheckpointFailure(context.Background(), "sid-cp-fail"); err != nil {
			t.Fatalf("RecordCheckpointFailure: %v", err)
		}
	}

	got, _ := store.Get(context.Background(), "sid-cp-fail")
	if got.CheckpointSaveFailures != 3 {
		t.Errorf("CheckpointSaveFailures = %d, want 3", got.CheckpointSaveFailures)
	}
}

// TestMetricsHook_NilStore_NoOp ensures the hook can be installed with
// a nil store as a defensive default — important for harnesses that
// build hook chains before deciding whether metrics persistence is
// turned on.
func TestMetricsHook_NilStore_NoOp(t *testing.T) {
	h := NewSessionMetricsHook(nil)

	ev := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid",
		schema.LLMCallEndData{PromptTokens: 1, CompletionTokens: 1})
	if err := h.OnEvent(context.Background(), ev); err != nil {
		t.Errorf("OnEvent (nil store): %v", err)
	}
	if err := h.RecordResume(context.Background(), "sid"); err != nil {
		t.Errorf("RecordResume (nil store): %v", err)
	}
	if err := h.RecordCheckpointFailure(context.Background(), "sid"); err != nil {
		t.Errorf("RecordCheckpointFailure (nil store): %v", err)
	}
}

// TestMetricsHook_EmptySessionID_Skipped ensures the hook does not
// record metrics for events that lack a session id — those are
// process-global and have no aggregation target.
func TestMetricsHook_EmptySessionID_Skipped(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	ev := schema.NewEvent(schema.EventLLMCallEnd, "agent", "", /* no sid */
		schema.LLMCallEndData{PromptTokens: 1})
	if err := h.OnEvent(context.Background(), ev); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	// No record should have been created.
	for _, sid := range []string{"", "sid", "agent"} {
		if _, err := store.Get(context.Background(), sid); err == nil {
			t.Errorf("found unexpected record for %q", sid)
		}
	}
}

// TestMetricsHook_WrongPayloadType_Ignored guards the type-assertion
// branches against future event-payload churn: if a type-assertion
// fails the hook simply skips, no panic.
func TestMetricsHook_WrongPayloadType_Ignored(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store)

	bogus := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid-bogus",
		schema.AgentEndData{Duration: 100}) // wrong payload type for this event

	if err := h.OnEvent(context.Background(), bogus); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	if _, err := store.Get(context.Background(), "sid-bogus"); err == nil {
		t.Error("expected no record after wrong-payload event")
	}
}

// TestMetricsHook_LoggerOption smoke-checks that WithMetricsLogger
// actually replaces the default. We can't easily intercept slog
// output here without redirecting handler, so just verify
// construction does not panic and the option is honoured.
func TestMetricsHook_LoggerOption(t *testing.T) {
	store := NewMapMetricsStore()
	h := NewSessionMetricsHook(store, WithMetricsLogger(nil)) // nil → keep default

	if h.logger == nil {
		t.Fatal("nil logger overrode default — should be guarded")
	}
}

// TestMetricsHook_TimeZeroLLMEvent ensures an LLM-call payload with
// zero tokens still records via Update (the call happened, even if
// degenerate). FirstSeen is then populated and counters stay at 0.
func TestMetricsHook_TimeZeroLLMEvent(t *testing.T) {
	clk := newFixedClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	store := NewMapMetricsStore().WithClock(clk.Now)
	h := NewSessionMetricsHook(store)

	ev := schema.NewEvent(schema.EventLLMCallEnd, "agent", "sid-zero-llm",
		schema.LLMCallEndData{Model: "test-model"}) // 0 tokens, no cost

	if err := h.OnEvent(context.Background(), ev); err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	got, err := store.Get(context.Background(), "sid-zero-llm")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.FirstSeen.Equal(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("FirstSeen = %v, want seed time", got.FirstSeen)
	}
}
