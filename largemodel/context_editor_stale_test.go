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
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// fakeReadTool returns a single read-mode ref to the file_path arg.
type fakeReadTool struct{}

func (fakeReadTool) ResourceIDs(args map[string]any) []tool.ResourceRef {
	p, _ := args["file_path"].(string)
	if p == "" {
		return nil
	}
	return []tool.ResourceRef{{ID: p, Mode: tool.ResourceRead}}
}

// fakeWriteTool returns a single write-mode ref to the file_path arg.
type fakeWriteTool struct{}

func (fakeWriteTool) ResourceIDs(args map[string]any) []tool.ResourceRef {
	p, _ := args["file_path"].(string)
	if p == "" {
		return nil
	}
	return []tool.ResourceRef{{ID: p, Mode: tool.ResourceWrite}}
}

// staleLookup returns trackers for "read" and "write" tool names. Other
// names get nil to exercise the "tracker not registered" branch.
func staleLookup(name string) tool.ResourceTracker {
	switch name {
	case "read":
		return fakeReadTool{}
	case "write":
		return fakeWriteTool{}
	}
	return nil
}

// jsonArgs marshals a map of args to the format ToolCall.Function.Arguments
// would carry (a JSON string).
func jsonArgs(t *testing.T, m map[string]any) string {
	t.Helper()
	switch len(m) {
	case 0:
		return ""
	case 1:
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				t.Fatalf("jsonArgs only supports single string-valued args, got %T", v)
			}
			return `{"` + k + `":"` + s + `"}`
		}
	}
	t.Fatalf("jsonArgs only supports 1-key maps, got %d keys", len(m))
	return ""
}

// reactReq builds an alternating assistant/tool sequence for one or
// more turns. Each turn supplies the assistant's tool_calls and the
// matching tool_results; tests can then assert which results get
// folded.
type turn struct {
	calls   []aimodel.ToolCall
	results []aimodel.Message // RoleTool entries; ToolCallID must match a call.ID
}

func buildReact(t *testing.T, turns []turn) *aimodel.ChatRequest {
	t.Helper()
	msgs := []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}
	for _, tn := range turns {
		msgs = append(msgs, aimodel.Message{
			Role:      aimodel.RoleAssistant,
			ToolCalls: tn.calls,
		})
		msgs = append(msgs, tn.results...)
	}
	return &aimodel.ChatRequest{Model: "test", Messages: msgs}
}

// dispatchCapture records the most recent EventContextEdited payload
// dispatched by the middleware, plus a count of total dispatches so a
// test can detect double-fires.
type dispatchCapture struct {
	count   int
	last    schema.Event
	payload schema.ContextEditedData
}

func (d *dispatchCapture) record(_ context.Context, e schema.Event) {
	d.count++
	d.last = e
	if p, ok := e.Data.(schema.ContextEditedData); ok {
		d.payload = p
	}
}

func mkRead(callID, path string, t *testing.T) (aimodel.ToolCall, aimodel.Message) {
	tc := aimodel.ToolCall{
		ID: callID,
		Function: aimodel.FunctionCall{
			Name:      "read",
			Arguments: jsonArgs(t, map[string]any{"file_path": path}),
		},
	}
	r := aimodel.Message{
		Role:       aimodel.RoleTool,
		ToolCallID: callID,
		Content:    aimodel.NewTextContent(strings.Repeat("x", 100)),
	}
	return tc, r
}

func mkWrite(callID, path string, t *testing.T) (aimodel.ToolCall, aimodel.Message) {
	tc := aimodel.ToolCall{
		ID: callID,
		Function: aimodel.FunctionCall{
			Name:      "write",
			Arguments: jsonArgs(t, map[string]any{"file_path": path}),
		},
	}
	r := aimodel.Message{
		Role:       aimodel.RoleTool,
		ToolCallID: callID,
		Content:    aimodel.NewTextContent("ok"),
	}
	return tc, r
}

// TestStale_ReadThenWriteSamePath: the earliest read of /a is shadowed
// by a later write to /a; the read tool_result is folded with reason
// stale_resource even though keep_last_k alone would not have touched
// it.
func TestStale_ReadThenWriteSamePath(t *testing.T) {
	cap := &captureCompleter{}
	disp := &dispatchCapture{}

	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50), // keep_last_k inactive — only stale should fire
		WithStaleResourceTracker(staleLookup),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)
	w1, wr1 := mkWrite("c2", "/a", t)
	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1}, results: []aimodel.Message{rr1}},
		{calls: []aimodel.ToolCall{w1}, results: []aimodel.Message{wr1}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	rr1Idx := findToolCallResult(got, "c1")
	wr1Idx := findToolCallResult(got, "c2")

	if !strings.Contains(got[rr1Idx].Content.Text(), "stale_resource") {
		t.Errorf("c1 result not folded as stale: %q", got[rr1Idx].Content.Text())
	}
	if !strings.Contains(got[rr1Idx].Content.Text(), "/a") {
		t.Errorf("c1 placeholder missing path detail: %q", got[rr1Idx].Content.Text())
	}
	if !strings.Contains(got[rr1Idx].Content.Text(), "c2") {
		t.Errorf("c1 placeholder missing writer call ID: %q", got[rr1Idx].Content.Text())
	}
	if got[wr1Idx].Content.Text() == "" || strings.Contains(got[wr1Idx].Content.Text(), "context_edited") {
		t.Errorf("write result was folded but should be kept: %q", got[wr1Idx].Content.Text())
	}

	if disp.count != 1 {
		t.Errorf("dispatch count = %d, want 1", disp.count)
	}
	if disp.payload.Strategy != contextEditStrategyStaleResource {
		t.Errorf("Strategy = %q, want %q", disp.payload.Strategy, contextEditStrategyStaleResource)
	}
	if disp.payload.Edited != 1 {
		t.Errorf("Edited = %d, want 1", disp.payload.Edited)
	}
}

// TestStale_MultipleReadsOneWrite: 3 reads of the same path then one
// write — all 3 reads are stale.
func TestStale_MultipleReadsOneWrite(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)
	r2, rr2 := mkRead("c2", "/a", t)
	r3, rr3 := mkRead("c3", "/a", t)
	w, wr := mkWrite("cw", "/a", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1, r2, r3}, results: []aimodel.Message{rr1, rr2, rr3}},
		{calls: []aimodel.ToolCall{w}, results: []aimodel.Message{wr}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	for _, id := range []string{"c1", "c2", "c3"} {
		idx := findToolCallResult(got, id)
		if !strings.Contains(got[idx].Content.Text(), "stale_resource") {
			t.Errorf("%s result not stale-folded: %q", id, got[idx].Content.Text())
		}
	}
}

// TestStale_FreshReadAfterWriteIsKept: read → write → read same path —
// only the first read is stale; the post-write read survives.
func TestStale_FreshReadAfterWriteIsKept(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)
	w, wr := mkWrite("cw", "/a", t)
	r2, rr2 := mkRead("c3", "/a", t)
	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1}, results: []aimodel.Message{rr1}},
		{calls: []aimodel.ToolCall{w}, results: []aimodel.Message{wr}},
		{calls: []aimodel.ToolCall{r2}, results: []aimodel.Message{rr2}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx1 := findToolCallResult(got, "c1")
	idx2 := findToolCallResult(got, "c3")

	if !strings.Contains(got[idx1].Content.Text(), "stale_resource") {
		t.Errorf("first read should be stale: %q", got[idx1].Content.Text())
	}
	if strings.Contains(got[idx2].Content.Text(), "context_edited") {
		t.Errorf("post-write read should be kept verbatim: %q", got[idx2].Content.Text())
	}
}

// TestStale_DisabledByDefault: no lookup ⇒ stale pass is silent and
// only keep_last_k can elide; the wire-format placeholder remains the
// V1 default so callers depending on it stay green.
func TestStale_DisabledByDefault(t *testing.T) {
	cap := &captureCompleter{}
	disp := &dispatchCapture{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	// 4 reads of distinct paths + 1 write to a path no read touched.
	// keep_last_k will fold the oldest 2 reads; stale must contribute
	// nothing because the lookup is nil.
	r1, rr1 := mkRead("c1", "/a", t)
	r2, rr2 := mkRead("c2", "/b", t)
	r3, rr3 := mkRead("c3", "/c", t)
	r4, rr4 := mkRead("c4", "/d", t)
	w, wr := mkWrite("cw", "/e", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1, r2, r3, r4}, results: []aimodel.Message{rr1, rr2, rr3, rr4}},
		{calls: []aimodel.ToolCall{w}, results: []aimodel.Message{wr}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx1 := findToolCallResult(got, "c1")
	if !strings.Contains(got[idx1].Content.Text(), "context_edited") {
		t.Errorf("c1 should be folded by keep_last_k: %q", got[idx1].Content.Text())
	}
	// V1 wire-format check — the legacy placeholder must NOT mention
	// "stale_resource" or use parens around a reason.
	if strings.Contains(got[idx1].Content.Text(), "stale_resource") {
		t.Errorf("legacy placeholder leaked stale: %q", got[idx1].Content.Text())
	}
	if strings.Contains(got[idx1].Content.Text(), "(keep_last_k)") {
		t.Errorf("legacy placeholder leaked V2 format: %q", got[idx1].Content.Text())
	}
	if disp.payload.Strategy != contextEditStrategyKeepLastK {
		t.Errorf("Strategy = %q, want %q", disp.payload.Strategy, contextEditStrategyKeepLastK)
	}
}

// TestStale_NoWritesNoFold: lookup is wired but no write tool_call ever
// fires — stale contributes nothing, keep_last_k still applies. Sanity
// check that the resource pass is cheap when it has no signal.
func TestStale_NoWritesNoFold(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)
	r2, rr2 := mkRead("c2", "/a", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1, r2}, results: []aimodel.Message{rr1, rr2}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	for _, id := range []string{"c1", "c2"} {
		idx := findToolCallResult(got, id)
		if strings.Contains(got[idx].Content.Text(), "context_edited") {
			t.Errorf("%s should be untouched without writes: %q", id, got[idx].Content.Text())
		}
	}
}

// TestStale_MalformedArgsTolerated: a write call with bad JSON args is
// quietly ignored — it does not enter the writes table and therefore
// does not invalidate prior reads.
func TestStale_MalformedArgsTolerated(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)

	badWrite := aimodel.ToolCall{
		ID: "bad",
		Function: aimodel.FunctionCall{
			Name:      "write",
			Arguments: "not-valid-json",
		},
	}
	badResult := aimodel.Message{
		Role:       aimodel.RoleTool,
		ToolCallID: "bad",
		Content:    aimodel.NewTextContent("err"),
	}

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1}, results: []aimodel.Message{rr1}},
		{calls: []aimodel.ToolCall{badWrite}, results: []aimodel.Message{badResult}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx := findToolCallResult(got, "c1")
	if strings.Contains(got[idx].Content.Text(), "context_edited") {
		t.Errorf("c1 should not be stale (write had bad args): %q", got[idx].Content.Text())
	}
}

// TestStale_StaleAndKeepLastKOverlap: the earliest read is *both* in
// the keep_last_k cut AND shadowed by a later write. The placeholder
// must surface stale_resource, not keep_last_k — it is the more
// informative reason. Edited count is the union (not the sum).
func TestStale_StaleAndKeepLastKOverlap(t *testing.T) {
	cap := &captureCompleter{}
	disp := &dispatchCapture{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2), // keep last 2 tool_results
		WithStaleResourceTracker(staleLookup),
		WithContextEditDispatch(disp.record),
	)
	wrapped := mw.Wrap(cap)

	// 4 reads (2 of /a, 2 of /b) then a write to /a.
	// keep_last_k would fold the first 2 (c1, c2). stale would fold
	// c1 (read /a, shadowed by /a write). Union = {c1, c2}.
	r1, rr1 := mkRead("c1", "/a", t)
	r2, rr2 := mkRead("c2", "/b", t)
	r3, rr3 := mkRead("c3", "/a", t)
	r4, rr4 := mkRead("c4", "/b", t)
	w, wr := mkWrite("cw", "/a", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1, r2, r3, r4}, results: []aimodel.Message{rr1, rr2, rr3, rr4}},
		{calls: []aimodel.ToolCall{w}, results: []aimodel.Message{wr}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx1 := findToolCallResult(got, "c1")
	idx2 := findToolCallResult(got, "c2")
	idx3 := findToolCallResult(got, "c3")

	// c1 must surface stale (more informative than keep_last_k).
	if !strings.Contains(got[idx1].Content.Text(), "stale_resource") {
		t.Errorf("c1 should be stale-folded, got %q", got[idx1].Content.Text())
	}
	// c2 was folded by keep_last_k (it reads /b, untouched by write).
	if !strings.Contains(got[idx2].Content.Text(), "keep_last_k") {
		t.Errorf("c2 should be keep_last_k-folded, got %q", got[idx2].Content.Text())
	}
	// c3 is fresh (read /a happened *after* the write would invalidate
	// older reads — but in our turn ordering c3 is in turn 1 and the
	// write is in turn 2, so c3 is older than the write and *also*
	// stale)... wait: c3 reads /a in turn 1 (assistantIdx=2) and the
	// write is in turn 2 (assistantIdx=4). So c3 IS shadowed too.
	// c4 reads /b which has no later write, and survives keep_last_k.
	if !strings.Contains(got[idx3].Content.Text(), "stale_resource") {
		t.Errorf("c3 (read /a in turn 1) should be stale: %q", got[idx3].Content.Text())
	}

	if disp.payload.Strategy != contextEditStrategyStaleResource {
		t.Errorf("Strategy = %q, want %q (stale takes precedence)", disp.payload.Strategy, contextEditStrategyStaleResource)
	}
	// Union = c1 + c2 + c3 = 3. (c4 kept by keep_last_k *and* not stale.)
	if disp.payload.Edited != 3 {
		t.Errorf("Edited = %d, want 3", disp.payload.Edited)
	}
}

// TestStale_PlaceholderV2OptIn: when the caller supplies a custom V2
// template, it receives the reason and detail.
func TestStale_PlaceholderV2OptIn(t *testing.T) {
	cap := &captureCompleter{}

	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
		WithPlaceholderV2(func(id string, n int, reason, detail string) string {
			return "PV2[" + id + "|" + reason + "|" + detail + "|" + itoa(n) + "]"
		}),
	)
	wrapped := mw.Wrap(cap)

	r1, rr1 := mkRead("c1", "/a", t)
	w, wr := mkWrite("cw", "/a", t)
	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1}, results: []aimodel.Message{rr1}},
		{calls: []aimodel.ToolCall{w}, results: []aimodel.Message{wr}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages[findToolCallResult(cap.gotChat.Messages, "c1")].Content.Text()
	if !strings.HasPrefix(got, "PV2[c1|stale_resource|file /a modified by cw|") {
		t.Errorf("V2 placeholder shape unexpected: %q", got)
	}
}

// TestStale_UnknownToolName: assistant calls a tool the lookup does
// not know — that call neither contributes a write nor is later
// considered as a read. No stale, no panic.
func TestStale_UnknownToolName(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(50),
		WithStaleResourceTracker(staleLookup),
	)
	wrapped := mw.Wrap(cap)

	mystery := aimodel.ToolCall{
		ID: "x1",
		Function: aimodel.FunctionCall{
			Name:      "mystery_tool",
			Arguments: `{"file_path":"/a"}`,
		},
	}
	mr := aimodel.Message{Role: aimodel.RoleTool, ToolCallID: "x1", Content: aimodel.NewTextContent("mystery")}

	r1, rr1 := mkRead("c1", "/a", t)

	req := buildReact(t, []turn{
		{calls: []aimodel.ToolCall{r1}, results: []aimodel.Message{rr1}},
		{calls: []aimodel.ToolCall{mystery}, results: []aimodel.Message{mr}},
	})

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	got := cap.gotChat.Messages
	idx := findToolCallResult(got, "c1")
	if strings.Contains(got[idx].Content.Text(), "context_edited") {
		t.Errorf("c1 should be intact when later tool is unknown: %q", got[idx].Content.Text())
	}
}

// findToolCallResult returns the index in msgs of the RoleTool message
// whose ToolCallID matches id. Fails the test if not found.
func findToolCallResult(msgs []aimodel.Message, id string) int {
	for i, m := range msgs {
		if m.Role == aimodel.RoleTool && m.ToolCallID == id {
			return i
		}
	}
	panic("tool result with id " + id + " not found")
}

// itoa returns a base-10 string for n. Local helper to avoid pulling
// strconv into the placeholder closure.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
