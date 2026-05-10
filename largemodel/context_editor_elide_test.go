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

package largemodel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
)

// memArtifactWriter records every Write call and lets a test inject an
// error to exercise the degraded-mode branch.
type memArtifactWriter struct {
	mu     sync.Mutex
	writes []memArtifactWrite
	err    error
}

type memArtifactWrite struct {
	sid     string
	name    string
	content []byte
}

func (w *memArtifactWriter) Write(_ context.Context, sid, name string, content []byte) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return "", w.err
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	w.writes = append(w.writes, memArtifactWrite{sid: sid, name: name, content: cp})
	return "/fake/" + sid + "/" + name, nil
}

func (w *memArtifactWriter) all() []memArtifactWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]memArtifactWrite, len(w.writes))
	copy(out, w.writes)
	return out
}

func staticSID(sid string) SessionIDFunc {
	return func(*aimodel.ChatRequest) string { return sid }
}

// TestElide_DisabledByDefault: without WithMaxBytesPerMessage no
// elision happens regardless of body size.
func TestElide_DisabledByDefault(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(50))
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(2, 100_000)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	// Pass-through expected.
	if &req.Messages[0] != &cap.gotChat.Messages[0] {
		t.Fatal("expected pass-through when elision disabled")
	}
}

// TestElide_HappyPath: oversized tool_result is externalised; the
// downstream prompt carries a "see artifacts/..." reference and the
// writer records the original bytes verbatim.
func TestElide_HappyPath(t *testing.T) {
	cap := &captureCompleter{}
	disp := &dispatchCapture{}
	w := &memArtifactWriter{}

	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	body := strings.Repeat("a", 5000)
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{{ID: "c1", Function: aimodel.FunctionCall{Name: "anything"}}}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx := findToolCallResult(got, "c1")
	text := got[idx].Content.Text()
	if !strings.Contains(text, ContextEditStrategyElideArtifact) {
		t.Errorf("placeholder missing reason: %q", text)
	}
	if !strings.Contains(text, "artifacts/elided-") {
		t.Errorf("placeholder missing artifact reference: %q", text)
	}
	if !strings.Contains(text, "KiB") {
		t.Errorf("placeholder missing human size: %q", text)
	}

	writes := w.all()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if writes[0].sid != "sess-1" {
		t.Errorf("write.sid = %q, want sess-1", writes[0].sid)
	}
	if !strings.HasPrefix(writes[0].name, "elided-") || !strings.HasSuffix(writes[0].name, ".txt") {
		t.Errorf("write.name shape unexpected: %q", writes[0].name)
	}
	if string(writes[0].content) != body {
		t.Errorf("write.content not preserved")
	}

	if disp.payload.Strategy != ContextEditStrategyElideArtifact {
		t.Errorf("Strategy = %q, want %q", disp.payload.Strategy, ContextEditStrategyElideArtifact)
	}
	if disp.payload.Edited != 1 {
		t.Errorf("Edited = %d, want 1", disp.payload.Edited)
	}
}

// TestElide_DegradeNoWriter: the body is over threshold but no writer
// is wired — the placeholder degrades to inline form, no panic.
func TestElide_DegradeNoWriter(t *testing.T) {
	cap := &captureCompleter{}
	disp := &dispatchCapture{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(1000),
		WithSessionIDFunc(staticSID("sess-1")),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	body := strings.Repeat("a", 5000)
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}}}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	text := cap.gotChat.Messages[findToolCallResult(cap.gotChat.Messages, "c1")].Content.Text()
	if !strings.Contains(text, "elide_inline") {
		t.Errorf("expected inline reason, got %q", text)
	}
	if !strings.Contains(text, "no artifact store") {
		t.Errorf("expected hint about missing store, got %q", text)
	}
	// Strategy should *not* be elide_to_artifact when nothing was
	// actually written.
	if disp.payload.Strategy == ContextEditStrategyElideArtifact {
		t.Errorf("Strategy = %q, want non-artifact (nothing written)", disp.payload.Strategy)
	}
}

// TestElide_DegradeNoSession: writer is wired but no session id can be
// resolved — same degraded path.
func TestElide_DegradeNoSession(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		// No SessionIDFunc => sid resolves to "".
	)
	wrapped := mw.Wrap(cap)

	body := strings.Repeat("a", 5000)
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}}}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if got := w.all(); len(got) != 0 {
		t.Errorf("expected no writes without session id, got %d", len(got))
	}
	text := cap.gotChat.Messages[findToolCallResult(cap.gotChat.Messages, "c1")].Content.Text()
	if !strings.Contains(text, "elide_inline") || !strings.Contains(text, "no artifact store") {
		t.Errorf("placeholder = %q", text)
	}
}

// TestElide_DegradeWriterError: writer returns an error — placeholder
// degrades to inline notice; original prompt is not corrupted; error
// is logged but the request still goes through.
func TestElide_DegradeWriterError(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{err: errors.New("disk full")}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
	)
	wrapped := mw.Wrap(cap)

	body := strings.Repeat("a", 5000)
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}}}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	text := cap.gotChat.Messages[findToolCallResult(cap.gotChat.Messages, "c1")].Content.Text()
	if !strings.Contains(text, "elide_inline") || !strings.Contains(text, "artifact write failed") {
		t.Errorf("placeholder = %q", text)
	}
}

// TestElide_BelowThreshold: bodies under maxBytesPerMessage are
// untouched even when elision is configured.
func TestElide_BelowThreshold(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(10_000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
	)
	wrapped := mw.Wrap(cap)

	body := strings.Repeat("a", 100) // 100 < 10000
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}}}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if &req.Messages[0] != &cap.gotChat.Messages[0] {
		t.Fatal("expected pass-through under threshold")
	}
	if got := w.all(); len(got) != 0 {
		t.Errorf("unexpected writes: %d", len(got))
	}
}

// TestElide_MultipleMessages: every oversized body triggers a separate
// write with a distinct hash-derived name (content-addressed); same
// content gets the same name (idempotent).
func TestElide_MultipleMessages(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
	)
	wrapped := mw.Wrap(cap)

	bodyA := strings.Repeat("a", 5000)
	bodyB := strings.Repeat("b", 5000)
	bodyADup := strings.Repeat("a", 5000) // identical to bodyA

	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{
			{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c2", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c3", Function: aimodel.FunctionCall{Name: "x"}},
		}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(bodyA)},
		{Role: aimodel.RoleTool, ToolCallID: "c2", Content: aimodel.NewTextContent(bodyB)},
		{Role: aimodel.RoleTool, ToolCallID: "c3", Content: aimodel.NewTextContent(bodyADup)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	writes := w.all()
	if len(writes) != 3 {
		t.Fatalf("expected 3 writes, got %d", len(writes))
	}

	if writes[0].name != writes[2].name {
		t.Errorf("identical content produced different names: %q vs %q",
			writes[0].name, writes[2].name)
	}
	if writes[0].name == writes[1].name {
		t.Errorf("distinct content collapsed to same name: %q", writes[0].name)
	}
}

// TestElide_LosesToKeepLastK_StillReportsArtifact: a single oversized
// tool_result is also in the keep_last_k cut. Per precedence, the
// elision reason wins for that index; strategy on the event is
// elide_to_artifact (most informative).
func TestElide_LosesToKeepLastK_StillReportsArtifact(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{}
	disp := &dispatchCapture{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	// 5 tool_results: index 0 is huge (5000 bytes), others are 100.
	// keep_last_k(2) keeps the last 2; folds indices 0,1,2.
	// elision marks index 0.
	// Expected per-index reason: 0 → elide_to_artifact;
	//                            1,2 → keep_last_k.
	body0 := strings.Repeat("a", 5000)
	short := strings.Repeat("b", 100)
	req := &aimodel.ChatRequest{Model: "test", Messages: []aimodel.Message{
		{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{
			{ID: "c1", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c2", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c3", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c4", Function: aimodel.FunctionCall{Name: "x"}},
			{ID: "c5", Function: aimodel.FunctionCall{Name: "x"}},
		}},
		{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent(body0)},
		{Role: aimodel.RoleTool, ToolCallID: "c2", Content: aimodel.NewTextContent(short)},
		{Role: aimodel.RoleTool, ToolCallID: "c3", Content: aimodel.NewTextContent(short)},
		{Role: aimodel.RoleTool, ToolCallID: "c4", Content: aimodel.NewTextContent(short)},
		{Role: aimodel.RoleTool, ToolCallID: "c5", Content: aimodel.NewTextContent(short)},
	}}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx0 := findToolCallResult(got, "c1")
	idx1 := findToolCallResult(got, "c2")

	t0 := got[idx0].Content.Text()
	t1 := got[idx1].Content.Text()

	if !strings.Contains(t0, ContextEditStrategyElideArtifact) {
		t.Errorf("c1 expected elide reason, got %q", t0)
	}
	if !strings.Contains(t1, contextEditStrategyKeepLastK) {
		t.Errorf("c2 expected keep_last_k reason, got %q", t1)
	}
	if disp.payload.Strategy != ContextEditStrategyElideArtifact {
		t.Errorf("Strategy = %q, want %q (artifact > stale > keep_last_k)",
			disp.payload.Strategy, ContextEditStrategyElideArtifact)
	}
	// 3 indices folded total (c1, c2, c3 — c4 c5 kept by keep_last_k).
	if disp.payload.Edited != 3 {
		t.Errorf("Edited = %d, want 3", disp.payload.Edited)
	}
}

// TestElide_ThreeStrategiesStack: same request triggers all three
// strategies — the dominant strategy on the event is elide; the
// per-index placeholders carry the right reasons.
func TestElide_ThreeStrategiesStack(t *testing.T) {
	cap := &captureCompleter{}
	w := &memArtifactWriter{}
	disp := &dispatchCapture{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2),
		WithStaleResourceTracker(staleLookup),
		WithMaxBytesPerMessage(1000),
		WithArtifactWriter(w),
		WithSessionIDFunc(staticSID("sess-1")),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	r1, _ := mkRead("c1", "/a", t) // read /a — will become stale
	rr1 := aimodel.Message{Role: aimodel.RoleTool, ToolCallID: "c1", Content: aimodel.NewTextContent("short read of /a")}
	r2, _ := mkRead("c2", "/b", t) // read /b — keep_last_k victim only
	rr2 := aimodel.Message{Role: aimodel.RoleTool, ToolCallID: "c2", Content: aimodel.NewTextContent("short read of /b")}
	r3, _ := mkRead("c3", "/c", t) // read /c — oversized -> elide
	rr3 := aimodel.Message{Role: aimodel.RoleTool, ToolCallID: "c3", Content: aimodel.NewTextContent(strings.Repeat("c", 5000))}
	r4, rr4 := mkRead("c4", "/d", t) // read /d — kept by keep_last_k
	r5, rr5 := mkRead("c5", "/e", t) // read /e — kept by keep_last_k
	w1, wr1 := mkWrite("cw", "/a", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1, r2, r3, r4, r5}, results: []aimodel.Message{rr1, rr2, rr3, rr4, rr5}},
		{calls: []aimodel.ToolCall{w1}, results: []aimodel.Message{wr1}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	t1 := got[findToolCallResult(got, "c1")].Content.Text()
	t2 := got[findToolCallResult(got, "c2")].Content.Text()
	t3 := got[findToolCallResult(got, "c3")].Content.Text()

	if !strings.Contains(t1, contextEditStrategyStaleResource) {
		t.Errorf("c1 expected stale, got %q", t1)
	}
	if !strings.Contains(t2, contextEditStrategyKeepLastK) {
		t.Errorf("c2 expected keep_last_k, got %q", t2)
	}
	if !strings.Contains(t3, ContextEditStrategyElideArtifact) {
		t.Errorf("c3 expected elide_to_artifact, got %q", t3)
	}

	if disp.payload.Strategy != ContextEditStrategyElideArtifact {
		t.Errorf("dominant Strategy = %q, want %q", disp.payload.Strategy, ContextEditStrategyElideArtifact)
	}
	// 6 tool_results total (c1..c5 + cw). keep_last_k(2) folds the
	// first 4 (c1, c2, c3, c4); stale adds c1 and elision adds c3 —
	// both already in the set. Union = {c1, c2, c3, c4} = 4.
	if disp.payload.Edited != 4 {
		t.Errorf("Edited = %d, want 4", disp.payload.Edited)
	}
}
