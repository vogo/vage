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

package vectorsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/vector"
)

// newRegistry returns an empty tool registry suitable for Register
// tests; mirrors the helper sessiontree's tests use, kept local to
// avoid cross-package coupling.
func newRegistry() *tool.Registry { return tool.NewRegistry() }

// fixture spins up a registry with both tools wired to fresh
// in-process backends. Callers exercise the registered handler the
// same way the agent runtime would.
func fixture(t *testing.T) (*tool.Registry, *vector.MapVectorStore, *vector.HashEmbedder) {
	t.Helper()
	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(64)
	reg := newRegistry()
	if err := Register(reg, store, emb); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg, store, emb
}

func callTool(t *testing.T, reg *tool.Registry, ctx context.Context, name, jsonArgs string) schema.ToolResult {
	t.Helper()
	if _, ok := reg.Get(name); !ok {
		t.Fatalf("tool %q not registered", name)
	}
	res, err := reg.Execute(ctx, name, jsonArgs)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return res
}

// resultText extracts the first text part from a ToolResult; tests use
// it to assert against the rendered string the LLM would see.
func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

func TestRegister_Validates(t *testing.T) {
	if err := Register(nil, vector.NewMapVectorStore(), vector.NewHashEmbedder(8)); err == nil {
		t.Error("expected nil registry to fail")
	}
	if err := Register(newRegistry(), nil, vector.NewHashEmbedder(8)); err == nil {
		t.Error("expected nil store to fail")
	}
	if err := Register(newRegistry(), vector.NewMapVectorStore(), nil); err == nil {
		t.Error("expected nil embedder to fail")
	}
}

func TestRegister_AllOrNothing(t *testing.T) {
	reg, _, _ := fixture(t)
	for _, name := range []string{SearchToolName, AddToolName} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not present after Register", name)
		}
	}
}

func TestVectorAdd_HappyPath(t *testing.T) {
	reg, store, _ := fixture(t)

	body, _ := json.Marshal(addArgs{Text: "alpha topic content body", Metadata: map[string]any{"topic": "alpha"}})
	res := callTool(t, reg, context.Background(), AddToolName, string(body))
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	docs, _ := store.List(context.Background())
	if docs[0].Text != "alpha topic content body" {
		t.Errorf("text round-trip wrong: %q", docs[0].Text)
	}
	if topic, _ := docs[0].Metadata["topic"].(string); topic != "alpha" {
		t.Errorf("metadata not stored: %+v", docs[0].Metadata)
	}
}

func TestVectorAdd_AutoIDIsRandom(t *testing.T) {
	reg, store, _ := fixture(t)

	for range 3 {
		body, _ := json.Marshal(addArgs{Text: "some unique-enough body text"})
		res := callTool(t, reg, context.Background(), AddToolName, string(body))
		if res.IsError {
			t.Fatalf("error: %s", resultText(res))
		}
	}
	if got := store.Len(); got != 3 {
		t.Errorf("expected 3 distinct docs, got Len=%d", got)
	}
}

func TestVectorAdd_AttachesSessionIDFromContext(t *testing.T) {
	reg, store, _ := fixture(t)

	ctx := schema.WithSessionID(context.Background(), "sess-42")
	body, _ := json.Marshal(addArgs{Text: "session-tagged body"})
	res := callTool(t, reg, ctx, AddToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	docs, _ := store.List(context.Background())
	if got, _ := docs[0].Metadata["session_id"].(string); got != "sess-42" {
		t.Errorf("session_id auto-tag missing: %+v", docs[0].Metadata)
	}
}

func TestVectorAdd_UserMetadataWinsOverSessionAuto(t *testing.T) {
	// If the LLM explicitly sets session_id, we should not overwrite it.
	reg, store, _ := fixture(t)

	ctx := schema.WithSessionID(context.Background(), "ctx-sid")
	body, _ := json.Marshal(addArgs{
		Text:     "explicit override body",
		Metadata: map[string]any{"session_id": "explicit"},
	})
	res := callTool(t, reg, ctx, AddToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	docs, _ := store.List(context.Background())
	if got, _ := docs[0].Metadata["session_id"].(string); got != "explicit" {
		t.Errorf("session_id override lost: %q", got)
	}
}

func TestVectorAdd_RejectsEmptyText(t *testing.T) {
	reg, _, _ := fixture(t)
	body, _ := json.Marshal(addArgs{Text: "   "})
	res := callTool(t, reg, context.Background(), AddToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for empty text")
	}
}

func TestVectorAdd_RejectsOversize(t *testing.T) {
	reg, _, _ := fixture(t)
	big := strings.Repeat("a", MaxTextBytes+1)
	body, _ := json.Marshal(addArgs{Text: big})
	res := callTool(t, reg, context.Background(), AddToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for oversize text")
	}
}

func TestVectorAdd_InvalidJSON(t *testing.T) {
	reg, _, _ := fixture(t)
	res := callTool(t, reg, context.Background(), AddToolName, "{ not json")
	if !res.IsError {
		t.Fatal("expected error for malformed json")
	}
}

func TestVectorSearch_HappyPath(t *testing.T) {
	reg, store, emb := fixture(t)

	// Seed three docs.
	for i, text := range []string{"alpha shared keyword tokens here", "beta shared keyword tokens here", "gamma orthogonal content"} {
		v, _ := emb.Embed(context.Background(), text)
		_ = store.Add(context.Background(), vector.Document{
			ID: idForIndex(i), Text: text, Embedding: v,
		})
	}

	body, _ := json.Marshal(searchArgs{Query: "alpha shared keyword tokens here", TopK: 2})
	res := callTool(t, reg, context.Background(), SearchToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	out := resultText(res)
	if !strings.Contains(out, "id=doc-0") {
		t.Errorf("top hit not in output: %s", out)
	}
	if !strings.Contains(out, "2 hit(s)") {
		t.Errorf("expected 2 hits in output, got: %s", out)
	}
}

func TestVectorSearch_NoHits(t *testing.T) {
	reg, _, _ := fixture(t)
	body, _ := json.Marshal(searchArgs{Query: "nothing matches"})
	res := callTool(t, reg, context.Background(), SearchToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "no hits") {
		t.Errorf("expected 'no hits' message, got %s", resultText(res))
	}
}

func TestVectorSearch_RequiresQuery(t *testing.T) {
	reg, _, _ := fixture(t)
	body, _ := json.Marshal(searchArgs{Query: ""})
	res := callTool(t, reg, context.Background(), SearchToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for empty query")
	}
}

func TestVectorSearch_TopKClamp(t *testing.T) {
	reg, store, emb := fixture(t)
	for i := range 60 {
		v, _ := emb.Embed(context.Background(), "doc body")
		_ = store.Add(context.Background(), vector.Document{ID: idForIndex(i), Text: "doc body", Embedding: v})
	}
	body, _ := json.Marshal(searchArgs{Query: "doc body", TopK: 999})
	res := callTool(t, reg, context.Background(), SearchToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	out := resultText(res)
	// Output should report 50 hits, not 999.
	if !strings.HasPrefix(out, "50 hit(s)") {
		t.Errorf("expected clamp to %d, output:\n%s", maxTopK, out)
	}
}

func TestSummarizeOneLine_Clip(t *testing.T) {
	long := strings.Repeat("xy ", 200)
	got := summarizeOneLine(long)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis, got: %s", got)
	}
}

func idForIndex(i int) string {
	switch i {
	case 0:
		return "doc-0"
	case 1:
		return "doc-1"
	case 2:
		return "doc-2"
	default:
		return "doc-" + intToString(i)
	}
}

func intToString(i int) string {
	// Avoid importing strconv just for tests when we already have fmt
	// in production code. Tiny helper.
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
