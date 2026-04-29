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

package vctx

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

// stubSource is a Source whose behaviour is fully driven by fixture fields.
// Tests use it to assert ordering, budget allocation, and error propagation
// without spinning up real memory or session stores.
type stubSource struct {
	name      string
	must      bool
	messages  []aimodel.Message
	err       error
	tokenCost int // when > 0, set FetchReport.Tokens explicitly
	seenInput *FetchInput
}

func (s *stubSource) Name() string { return s.name }

func (s *stubSource) MustInclude() bool { return s.must }

func (s *stubSource) Fetch(_ context.Context, in FetchInput) (FetchResult, error) {
	captured := in
	s.seenInput = &captured

	if s.err != nil {
		return FetchResult{Report: schema.ContextSourceReport{Source: s.name}}, s.err
	}

	rep := schema.ContextSourceReport{
		Source:  s.name,
		InputN:  len(s.messages),
		OutputN: len(s.messages),
	}
	if s.tokenCost > 0 {
		rep.Tokens = s.tokenCost
	}

	return FetchResult{Messages: append([]aimodel.Message(nil), s.messages...), Report: rep}, nil
}

// recordingHook captures every dispatched event for later inspection by
// the EventContextBuilt assertion.
type recordingHook struct {
	mu     sync.Mutex
	events []schema.Event
}

func (h *recordingHook) OnEvent(_ context.Context, ev schema.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	return nil
}

func (h *recordingHook) Filter() []string { return nil }

func (h *recordingHook) snapshot() []schema.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]schema.Event, len(h.events))
	copy(out, h.events)
	return out
}

// userMsg builds an aimodel user message with the given text. Used to
// keep test fixtures readable.
func userMsg(text string) aimodel.Message {
	return aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(text)}
}

// TestDefaultBuilder_BasicCompose verifies that messages from three sources
// emerge in declared order and the BuildReport tallies match.
func TestDefaultBuilder_BasicCompose(t *testing.T) {
	a := &stubSource{name: "a", must: true, messages: []aimodel.Message{userMsg("aa")}}
	b := &stubSource{name: "b", messages: []aimodel.Message{userMsg("bb")}}
	c := &stubSource{name: "c", must: true, messages: []aimodel.Message{userMsg("cc")}}

	builder := NewDefaultBuilder(WithSources(a, b, c))

	res, err := builder.Build(context.Background(), BuildInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(res.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(res.Messages))
	}

	got := []string{
		res.Messages[0].Content.Text(),
		res.Messages[1].Content.Text(),
		res.Messages[2].Content.Text(),
	}
	want := []string{"aa", "bb", "cc"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if res.Report.OutputCount != 3 {
		t.Errorf("OutputCount = %d, want 3", res.Report.OutputCount)
	}
	if len(res.Report.Sources) != 3 {
		t.Errorf("Sources len = %d, want 3", len(res.Report.Sources))
	}
	if res.Report.Strategy != StrategyOrderedGreedy {
		t.Errorf("Strategy = %q, want %q", res.Report.Strategy, StrategyOrderedGreedy)
	}
}

// TestDefaultBuilder_SourceErrorFailOpen verifies an optional source that
// returns an error does NOT stop the builder; later sources still run, and
// the failed source is recorded with Status="error".
func TestDefaultBuilder_SourceErrorFailOpen(t *testing.T) {
	a := &stubSource{name: "a", messages: []aimodel.Message{userMsg("ok")}}
	bad := &stubSource{name: "bad", err: errors.New("boom")}
	c := &stubSource{name: "c", messages: []aimodel.Message{userMsg("after")}}

	builder := NewDefaultBuilder(WithSources(a, bad, c))

	res, err := builder.Build(context.Background(), BuildInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(res.Messages) != 2 {
		t.Fatalf("expected 2 messages (a and c), got %d", len(res.Messages))
	}

	if res.Report.Sources[1].Status != StatusError {
		t.Errorf("bad source Status = %q, want %q", res.Report.Sources[1].Status, StatusError)
	}
	if res.Report.Sources[1].Error == "" {
		t.Errorf("bad source Error empty, want non-empty")
	}
	// c should still run after bad's failure.
	if c.seenInput == nil {
		t.Errorf("source c was never invoked after bad's failure")
	}
}

// TestDefaultBuilder_MustIncludeFailClosed verifies a must-include source's
// error propagates to the caller (system prompt failures should not be
// silently swallowed — they signal config bugs).
func TestDefaultBuilder_MustIncludeFailClosed(t *testing.T) {
	bad := &stubSource{name: "must", must: true, err: errors.New("render failed")}
	builder := NewDefaultBuilder(WithSource(bad))

	_, err := builder.Build(context.Background(), BuildInput{SessionID: "s1"})
	if err == nil {
		t.Fatalf("expected error from must-include source, got nil")
	}
	if !strings.Contains(err.Error(), "render failed") {
		t.Errorf("error missing root cause: %v", err)
	}
}

// TestDefaultBuilder_BudgetTrim verifies a positive Budget triggers head
// trimming when an optional source exceeds it. Message text length 16
// gives 4 estimated tokens each (memory.EstimateTextTokens uses len/4).
func TestDefaultBuilder_BudgetTrim(t *testing.T) {
	// 5 messages × 4 tokens = 20 total; budget 8 should keep the last 2.
	msgs := make([]aimodel.Message, 5)
	for i := range msgs {
		msgs[i] = userMsg(strings.Repeat("x", 16)) // 16 chars / 4 = 4 tokens
	}

	src := &stubSource{name: "history", messages: msgs}
	builder := NewDefaultBuilder(WithSource(src))

	res, err := builder.Build(context.Background(), BuildInput{SessionID: "s1", Budget: 8})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if got := len(res.Messages); got != 2 {
		t.Fatalf("expected 2 messages after trim, got %d", got)
	}
	rep := res.Report.Sources[0]
	if rep.Status != StatusTruncated {
		t.Errorf("Status = %q, want %q", rep.Status, StatusTruncated)
	}
	if rep.DroppedN != 3 {
		t.Errorf("DroppedN = %d, want 3", rep.DroppedN)
	}
	if res.Report.DroppedCount != 3 {
		t.Errorf("BuildReport.DroppedCount = %d, want 3", res.Report.DroppedCount)
	}
}

// TestDefaultBuilder_MustIncludeNotTrimmed verifies a tight Budget never
// drops must-include source output even when must-include alone exceeds
// the budget.
func TestDefaultBuilder_MustIncludeNotTrimmed(t *testing.T) {
	bigMsg := userMsg(strings.Repeat("x", 64)) // 16 tokens
	must := &stubSource{name: "must", must: true, messages: []aimodel.Message{bigMsg}}
	opt := &stubSource{name: "opt", messages: []aimodel.Message{userMsg("yyy")}}

	builder := NewDefaultBuilder(WithSources(must, opt))

	// Budget 8 < 16 must-include tokens → must-include still emits its
	// message, optional source gets 0 budget.
	res, err := builder.Build(context.Background(), BuildInput{SessionID: "s1", Budget: 8})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(res.Messages) < 1 {
		t.Fatalf("must-include message dropped; got %d messages", len(res.Messages))
	}
	if res.Messages[0].Content.Text() != bigMsg.Content.Text() {
		t.Errorf("first message != must-include output")
	}
}

// TestDefaultBuilder_MustIncludeAccountedFirst verifies optional sources
// see a budget reflecting the must-include token spend.
func TestDefaultBuilder_MustIncludeAccountedFirst(t *testing.T) {
	must := &stubSource{
		name: "must", must: true,
		messages:  []aimodel.Message{userMsg(strings.Repeat("x", 16))}, // 4 tokens
		tokenCost: 4,
	}
	opt := &stubSource{name: "opt", messages: []aimodel.Message{userMsg("aa")}}

	builder := NewDefaultBuilder(WithSources(must, opt))

	if _, err := builder.Build(context.Background(), BuildInput{SessionID: "s1", Budget: 10}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if opt.seenInput == nil {
		t.Fatal("optional source was never invoked")
	}
	if got := opt.seenInput.Budget; got != 6 {
		t.Errorf("optional Budget = %d, want 6 (10 - 4 must-include)", got)
	}
}

// TestDefaultBuilder_EmitsEvent verifies the Builder dispatches an
// EventContextBuilt event with a populated payload.
func TestDefaultBuilder_EmitsEvent(t *testing.T) {
	mgr := hook.NewManager()
	rec := &recordingHook{}
	mgr.Register(rec)

	a := &stubSource{name: "a", messages: []aimodel.Message{userMsg("hi")}}
	builder := NewDefaultBuilder(WithSource(a), WithHookManager(mgr), WithName("test"))

	if _, err := builder.Build(context.Background(), BuildInput{SessionID: "s1", AgentID: "agent"}); err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != schema.EventContextBuilt {
		t.Errorf("event type = %q, want %q", ev.Type, schema.EventContextBuilt)
	}
	data, ok := ev.Data.(schema.ContextBuiltData)
	if !ok {
		t.Fatalf("event data type = %T, want schema.ContextBuiltData", ev.Data)
	}
	if data.Builder != "test" {
		t.Errorf("Builder = %q, want %q", data.Builder, "test")
	}
	if data.OutputCount != 1 {
		t.Errorf("OutputCount = %d, want 1", data.OutputCount)
	}
	if len(data.Sources) != 1 {
		t.Errorf("Sources len = %d, want 1", len(data.Sources))
	}
}

// TestDefaultBuilder_NoSources verifies Build with zero sources returns
// an empty result (used in tests; no panic, no error).
func TestDefaultBuilder_NoSources(t *testing.T) {
	builder := NewDefaultBuilder()
	res, err := builder.Build(context.Background(), BuildInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.OutputCount != 0 {
		t.Errorf("OutputCount = %d, want 0", res.Report.OutputCount)
	}
}

// TestDefaultBuilder_ContextCancelled verifies a cancelled ctx short-
// circuits Build before any source runs.
func TestDefaultBuilder_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	src := stubSourceFunc(func() {
		called = true
	})

	builder := NewDefaultBuilder(WithSource(src))
	_, err := builder.Build(ctx, BuildInput{SessionID: "s1"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if called {
		t.Errorf("source was invoked despite cancelled context")
	}
}

// stubSourceFunc adapts a sentinel side-effect callback into a Source for
// the cancellation test.
func stubSourceFunc(onFetch func()) Source {
	return &cbSource{onFetch: onFetch}
}

type cbSource struct{ onFetch func() }

func (c *cbSource) Name() string { return "cb" }
func (c *cbSource) Fetch(_ context.Context, _ FetchInput) (FetchResult, error) {
	if c.onFetch != nil {
		c.onFetch()
	}
	return FetchResult{}, nil
}
