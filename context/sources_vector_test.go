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
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/vector"
)

// vectorTestSetup builds a HashEmbedder + MapVectorStore preloaded with
// three documents whose text deliberately overlaps with the test
// queries. Returning the embedder lets callers compose more docs.
func vectorTestSetup(t *testing.T) (*vector.MapVectorStore, *vector.HashEmbedder) {
	t.Helper()
	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(64)

	add := func(id, text string, md map[string]any) {
		t.Helper()
		v, err := emb.Embed(context.Background(), text)
		if err != nil {
			t.Fatalf("embed: %v", err)
		}
		err = store.Add(context.Background(), vector.Document{
			ID: id, Text: text, Embedding: v, Metadata: md,
		})
		if err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}

	add("a", "the quick brown fox jumps over the lazy dog", map[string]any{"kind": "fact"})
	add("b", "the brown fox runs through the field", map[string]any{"kind": "note"})
	add("c", "completely different unrelated tokens here", map[string]any{"kind": "fact"})
	return store, emb
}

func TestVectorRecallSource_HappyPath(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb, TopK: 2}

	res, err := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Report.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (note=%q)", res.Report.Status, StatusOK, res.Report.Note)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	if res.Messages[0].Role != aimodel.RoleSystem {
		t.Fatalf("role = %q, want system", res.Messages[0].Role)
	}
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "brown fox") {
		t.Fatalf("body missing expected text: %q", body)
	}
	if res.Report.OutputN != 1 {
		t.Errorf("OutputN = %d, want 1", res.Report.OutputN)
	}
	if res.Report.InputN < 1 {
		t.Errorf("InputN = %d, want >= 1", res.Report.InputN)
	}
}

func TestVectorRecallSource_SkippedOnNilStore(t *testing.T) {
	src := &VectorRecallSource{Embedder: vector.NewHashEmbedder(32)}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "x"})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped", res.Report.Status)
	}
}

func TestVectorRecallSource_SkippedOnNilEmbedder(t *testing.T) {
	src := &VectorRecallSource{Store: vector.NewMapVectorStore()}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "x"})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped", res.Report.Status)
	}
}

func TestVectorRecallSource_SkippedOnEmptyQuery(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb}
	// No Intent, no Request -> empty query.
	res, _ := src.Fetch(context.Background(), FetchInput{})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped", res.Report.Status)
	}
}

func TestVectorRecallSource_SkippedOnNoHits(t *testing.T) {
	emb := vector.NewHashEmbedder(32)
	store := vector.NewMapVectorStore()
	src := &VectorRecallSource{Store: store, Embedder: emb}

	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "anything"})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped (note=%q)", res.Report.Status, res.Report.Note)
	}
}

func TestVectorRecallSource_FailOpenOnEmbedError(t *testing.T) {
	store, _ := vectorTestSetup(t)
	failEmb := vector.EmbedderFunc(func(_ context.Context, _ string) ([]float32, error) {
		return nil, errors.New("boom")
	})
	src := &VectorRecallSource{Store: store, Embedder: failEmb}
	res, err := src.Fetch(context.Background(), FetchInput{Intent: "x"})
	if err != nil {
		t.Fatalf("expected nil error (fail-open), got %v", err)
	}
	if res.Report.Status != StatusError {
		t.Fatalf("Status = %q, want error", res.Report.Status)
	}
	if res.Report.Error == "" {
		t.Errorf("Error message should be set")
	}
}

func TestVectorRecallSource_FailOpenOnSearchError(t *testing.T) {
	emb := vector.NewHashEmbedder(32)
	src := &VectorRecallSource{Store: &errStore{err: errors.New("search-down")}, Embedder: emb}
	res, err := src.Fetch(context.Background(), FetchInput{Intent: "x"})
	if err != nil {
		t.Fatalf("expected nil error (fail-open), got %v", err)
	}
	if res.Report.Status != StatusError {
		t.Fatalf("Status = %q, want error", res.Report.Status)
	}
}

func TestVectorRecallSource_TopKRespected(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb, TopK: 1}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if res.Report.InputN != 1 {
		t.Fatalf("InputN = %d, want 1 (TopK should cap)", res.Report.InputN)
	}
}

func TestVectorRecallSource_MetadataEqualsForwarded(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{
		Store:          store,
		Embedder:       emb,
		MetadataEquals: map[string]any{"kind": "note"},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if res.Report.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", res.Report.Status)
	}
	body := res.Messages[0].Content.Text()
	// Only doc "b" has kind=note in the fixture, so doc "a" must not appear.
	if strings.Contains(body, "lazy dog") {
		t.Fatalf("doc with kind=fact leaked through MetadataEquals: %q", body)
	}
}

func TestVectorRecallSource_PredicateForwarded(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{
		Store:    store,
		Embedder: emb,
		Predicate: func(d vector.Document) bool {
			return d.ID == "a"
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "lazy dog") {
		t.Fatalf("expected doc a in body: %q", body)
	}
	if strings.Contains(body, "field") {
		t.Fatalf("predicate should have filtered out doc b: %q", body)
	}
}

func TestVectorRecallSource_QueryFnFallbackToLastUserMsg(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb}

	req := &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage("first message about cooking"),
			// Tool message would normally be skipped — simulate via
			// non-user role.
			{Message: aimodel.Message{
				Role:    aimodel.RoleAssistant,
				Content: aimodel.NewTextContent("intermediate"),
			}},
			schema.NewUserMessage("brown fox"),
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Request: req})
	if res.Report.Status != StatusOK {
		t.Fatalf("Status = %q, want ok (note=%q)", res.Report.Status, res.Report.Note)
	}
	// Body should be derived from "brown fox" — doc "a" wins.
	if !strings.Contains(res.Messages[0].Content.Text(), "lazy dog") {
		t.Fatalf("expected fox doc to win; got %q", res.Messages[0].Content.Text())
	}
}

func TestVectorRecallSource_QueryFnSkipsEmptyUserMsgs(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb}

	req := &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage("brown fox"),
			schema.NewUserMessage("   "), // whitespace-only, should be skipped
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Request: req})
	if res.Report.Status != StatusOK {
		t.Fatalf("Status = %q, want ok (whitespace-only user msg should not block fallback)", res.Report.Status)
	}
}

func TestVectorRecallSource_QueryFnNilRequestGraceful(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb}
	res, _ := src.Fetch(context.Background(), FetchInput{Request: nil})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped", res.Report.Status)
	}
}

func TestVectorRecallSource_CustomQueryFn(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{
		Store:    store,
		Embedder: emb,
		QueryFn: func(in FetchInput) string {
			return "lazy dog"
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "irrelevant"})
	if res.Report.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", res.Report.Status)
	}
}

func TestVectorRecallSource_RendererSeesFetchInput(t *testing.T) {
	store, emb := vectorTestSetup(t)
	got := struct {
		intent string
		hits   int
	}{}
	src := &VectorRecallSource{
		Store:    store,
		Embedder: emb,
		Render: func(in FetchInput, hits []vector.SearchHit) string {
			got.intent = in.Intent
			got.hits = len(hits)
			return "rendered: " + in.Intent
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if got.intent != "brown fox" {
		t.Errorf("renderer saw intent = %q, want brown fox", got.intent)
	}
	if got.hits == 0 {
		t.Errorf("renderer saw 0 hits; expected > 0")
	}
	if !strings.Contains(res.Messages[0].Content.Text(), "rendered: brown fox") {
		t.Errorf("body = %q, want custom render output", res.Messages[0].Content.Text())
	}
}

func TestVectorRecallSource_RenderPanicRecovered(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{
		Store:    store,
		Embedder: emb,
		Render: func(_ FetchInput, _ []vector.SearchHit) string {
			panic("renderer boom")
		},
	}
	res, err := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if err != nil {
		t.Fatalf("expected nil error (fail-open on panic), got %v", err)
	}
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped after renderer panic", res.Report.Status)
	}
}

func TestVectorRecallSource_RenderEmptySkipped(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{
		Store:    store,
		Embedder: emb,
		Render: func(_ FetchInput, _ []vector.SearchHit) string {
			return ""
		},
	}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox"})
	if res.Report.Status != StatusSkipped {
		t.Fatalf("Status = %q, want skipped", res.Report.Status)
	}
}

func TestVectorRecallSource_MaxBytesPerHit(t *testing.T) {
	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(32)
	long := strings.Repeat("alpha beta ", 200)
	v, _ := emb.Embed(context.Background(), long)
	_ = store.Add(context.Background(), vector.Document{ID: "x", Text: long, Embedding: v})

	src := &VectorRecallSource{Store: store, Embedder: emb, MaxBytesPerHit: 64}
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "alpha"})
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "[truncated]") {
		t.Fatalf("expected truncation marker in body, got %q", body)
	}
}

func TestVectorRecallSource_SelfTrim_DropsLowestScore(t *testing.T) {
	store, emb := vectorTestSetup(t)
	// Budget chosen to require at least one hit drop while leaving
	// enough room for the preamble + 1-2 surviving hits.
	src := &VectorRecallSource{Store: store, Embedder: emb, TopK: 3}
	const budget = 50
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "brown fox", Budget: budget})
	if res.Report.Status != StatusTruncated {
		t.Fatalf("Status = %q, want truncated (note=%q)", res.Report.Status, res.Report.Note)
	}
	if res.Report.DroppedN == 0 {
		t.Fatalf("expected DroppedN > 0; report = %+v", res.Report)
	}
	if res.Report.Tokens > budget {
		t.Fatalf("Tokens=%d should be <= budget=%d", res.Report.Tokens, budget)
	}
}

func TestVectorRecallSource_SelfTrim_FinalCharTruncate(t *testing.T) {
	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(32)
	long := strings.Repeat("alpha ", 500)
	v, _ := emb.Embed(context.Background(), long)
	_ = store.Add(context.Background(), vector.Document{ID: "x", Text: long, Embedding: v})

	src := &VectorRecallSource{Store: store, Embedder: emb}
	// Budget too small for the full doc; 50 leaves room for preamble +
	// a clamped tail of the long text.
	const budget = 50
	res, _ := src.Fetch(context.Background(), FetchInput{Intent: "alpha", Budget: budget})
	if res.Report.Status != StatusTruncated {
		t.Fatalf("Status = %q, want truncated; note=%q", res.Report.Status, res.Report.Note)
	}
	if res.Report.Tokens > budget {
		t.Fatalf("Tokens=%d should be <= budget=%d", res.Report.Tokens, budget)
	}
	// A truncated body should still contain the truncation marker.
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "[truncated]") {
		t.Fatalf("expected truncation marker; got %q", body)
	}
}

func TestVectorRecallSource_BuilderIntegration(t *testing.T) {
	store, emb := vectorTestSetup(t)
	src := &VectorRecallSource{Store: store, Embedder: emb, TopK: 2}

	builder := NewDefaultBuilder(WithSource(src))
	res, err := builder.Build(context.Background(), BuildInput{
		AgentID: "a", SessionID: "s", Intent: "brown fox",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message from builder, got %d", len(res.Messages))
	}
	if len(res.Report.Sources) != 1 {
		t.Fatalf("expected 1 source report, got %d", len(res.Report.Sources))
	}
	if res.Report.Sources[0].Source != SourceNameVectorRecall {
		t.Fatalf("source name = %q, want %q", res.Report.Sources[0].Source, SourceNameVectorRecall)
	}
}

// errStore implements vector.VectorStore but always returns the
// configured error from Search. Used to test fail-open paths.
type errStore struct {
	err error
}

func (e *errStore) Add(_ context.Context, _ vector.Document) error { return e.err }
func (e *errStore) Search(_ context.Context, _ []float32, _ vector.SearchOptions) ([]vector.SearchHit, error) {
	return nil, e.err
}
func (e *errStore) Delete(_ context.Context, _ string) error          { return e.err }
func (e *errStore) List(_ context.Context) ([]vector.Document, error) { return nil, e.err }
