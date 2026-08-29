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

// Package vector_tests is the integration suite for the qdrant backend
// plus the embedding providers. Tests skip cleanly when QDRANT_URL or the
// relevant vendor key is unset, mirroring the rest of vage/integrations/.
//
// The suite runs as a per-provider matrix: each embedding test executes
// once for OpenAI and once for Voyage, and an unconfigured vendor only
// skips its own subtests. Every subtest pins an explicit model and a
// shared output dimension, and writes into a fresh collection, so a
// provider swap never lands vectors from two models in one collection.
//
// Run locally with:
//
//	docker run --rm -p 6333:6333 qdrant/qdrant
//	export QDRANT_URL=http://localhost:6333
//	export OPENAI_API_KEY=sk-...
//	export VOYAGE_API_KEY=pa-...
//	go test ./integrations/vector_tests/... -v
package vector_tests //nolint:revive // integration test package

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/vogo/vage/vector"
	"github.com/vogo/vage/vector/provider/openais"
	"github.com/vogo/vage/vector/provider/voyages"
	"github.com/vogo/vage/vector/qdrant"
)

// embedDim is the output dimension every provider in the matrix is
// pinned to. Both vendors support server-side reduction to it, which
// keeps the qdrant collections comparable across providers and keeps the
// dimension-alignment test meaningful.
const embedDim = 256

// openAIModel and voyageModel are pinned explicitly rather than left to
// the provider defaults: an integration run should embed with a known
// model, and Voyage requires the field anyway.
const (
	openAIModel = "text-embedding-3-small"
	voyageModel = "voyage-3.5"
)

// providerCase is one row of the embedding matrix. newEmbedder returns a
// live embedder or skips the subtest when the vendor key is absent.
type providerCase struct {
	name        string
	newEmbedder func(t *testing.T, dim int) vector.Embedder
}

// providerMatrix enumerates the embedding backends under integration
// test. Adding a provider here extends every embedding test below.
func providerMatrix() []providerCase {
	return []providerCase{
		{name: "openai", newEmbedder: newOpenAIEmbedder},
		{name: "voyage", newEmbedder: newVoyageEmbedder},
	}
}

// newOpenAIEmbedder builds a live OpenAI embedder, skipping when no key
// is configured.
func newOpenAIEmbedder(t *testing.T, dim int) vector.Embedder {
	t.Helper()
	key := lookupKey("OPENAI_API_KEY", "VAGE_VECTOR_OPENAI_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY (or VAGE_VECTOR_OPENAI_KEY) not set; skipping OpenAI embedder integration test")
	}
	emb, err := openais.New(
		openais.WithAPIKey(key),
		openais.WithModel(openAIModel),
		openais.WithDimensions(dim),
	)
	if err != nil {
		t.Fatalf("openais.New: %v", err)
	}
	return emb
}

// newVoyageEmbedder builds a live Voyage embedder, skipping when no key
// is configured. Voyage is the embedding service Anthropic points Claude
// applications at, so it is the Claude-side half of the matrix.
func newVoyageEmbedder(t *testing.T, dim int) vector.Embedder {
	t.Helper()
	key := lookupKey("VOYAGE_API_KEY", "VAGE_VECTOR_VOYAGE_KEY")
	if key == "" {
		t.Skip("VOYAGE_API_KEY (or VAGE_VECTOR_VOYAGE_KEY) not set; skipping Voyage embedder integration test")
	}
	emb, err := voyages.New(
		voyages.WithAPIKey(key),
		voyages.WithModel(voyageModel),
		voyages.WithOutputDimension(dim),
	)
	if err != nil {
		t.Fatalf("voyages.New: %v", err)
	}
	return emb
}

// lookupKey returns the first non-empty environment variable among names.
func lookupKey(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// batchEmbed uses the BatchEmbedder capability, which every real provider
// implements; it fails the test rather than looping Embed, because the
// batch path is exactly what the integration run is here to exercise.
func batchEmbed(ctx context.Context, t *testing.T, emb vector.Embedder, texts []string) [][]float32 {
	t.Helper()
	be, ok := emb.(vector.BatchEmbedder)
	if !ok {
		t.Fatalf("%T does not implement vector.BatchEmbedder", emb)
	}
	vs, err := be.BatchEmbed(ctx, texts)
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	return vs
}

// uniqueCollection generates a per-test collection so parallel CI runs
// do not collide on a shared qdrant instance. The integration test
// deletes the collection at end-of-test by removing all points; we do
// not delete the collection itself to keep the qdrant API surface
// minimal for this package (no DELETE /collections endpoint wrapper).
func uniqueCollection(name string) string {
	return fmt.Sprintf("vage_it_%s_%d", name, time.Now().UnixNano())
}

func skipUnlessQdrant(t *testing.T) string {
	t.Helper()
	url := os.Getenv("QDRANT_URL")
	if url == "" {
		t.Skip("QDRANT_URL not set; skipping qdrant integration test")
	}
	return url
}

// TestRoundTrip is the end-to-end happy path, run once per provider:
// embed three texts, add them, search for the closest, and verify the
// closest hit has the expected document ID. Validates the full chain —
// collection auto-create, dimension inference, payload round-trip,
// ranking — against a real embedding service.
func TestRoundTrip(t *testing.T) {
	url := skipUnlessQdrant(t)

	for _, pc := range providerMatrix() {
		t.Run(pc.name, func(t *testing.T) {
			emb := pc.newEmbedder(t, embedDim)

			ctx := context.Background()
			col := uniqueCollection("roundtrip_" + pc.name)

			store, err := qdrant.New(url, col)
			if err != nil {
				t.Fatalf("qdrant.New: %v", err)
			}
			t.Cleanup(func() { dropCollection(t, url, col) })

			docs := []vector.Document{
				{ID: "go-routine", Text: "Go's goroutines are lightweight concurrent execution units"},
				{ID: "py-asyncio", Text: "Python asyncio uses an event loop for cooperative concurrency"},
				{ID: "irrelevant", Text: "Bananas are a popular tropical fruit"},
			}
			for i := range docs {
				v, err := emb.Embed(ctx, docs[i].Text)
				if err != nil {
					t.Fatalf("Embed[%d]: %v", i, err)
				}
				docs[i].Embedding = v
				if err := store.Add(ctx, docs[i]); err != nil {
					t.Fatalf("Add[%d]: %v", i, err)
				}
			}

			if got := store.LockedDimension(); got != embedDim {
				t.Errorf("LockedDimension = %d, want %d (first-Add lock to embedder dim)", got, embedDim)
			}

			queryVec, err := emb.Embed(ctx, "How does Go achieve concurrency?")
			if err != nil {
				t.Fatalf("query Embed: %v", err)
			}
			hits, err := store.Search(ctx, queryVec, vector.SearchOptions{TopK: 2})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) == 0 {
				t.Fatal("Search returned no hits")
			}
			if hits[0].Document.ID != "go-routine" {
				t.Errorf("top hit = %q, want %q (hits=%+v)", hits[0].Document.ID, "go-routine", briefHits(hits))
			}
			if hits[0].Document.Text == "" {
				t.Error("payload text not round-tripped")
			}
		})
	}
}

// TestBatchEmbed exercises the BatchEmbedder path per provider. The
// hits we get back must contain every doc we batched in, in the order
// the texts were submitted.
func TestBatchEmbed(t *testing.T) {
	url := skipUnlessQdrant(t)

	for _, pc := range providerMatrix() {
		t.Run(pc.name, func(t *testing.T) {
			emb := pc.newEmbedder(t, embedDim)

			ctx := context.Background()
			col := uniqueCollection("batch_" + pc.name)

			store, err := qdrant.New(url, col)
			if err != nil {
				t.Fatalf("qdrant.New: %v", err)
			}
			t.Cleanup(func() { dropCollection(t, url, col) })

			texts := []string{"alpha topic one", "beta topic two", "gamma topic three", "delta topic four"}
			vectors := batchEmbed(ctx, t, emb, texts)
			if len(vectors) != len(texts) {
				t.Fatalf("vectors len = %d, want %d", len(vectors), len(texts))
			}
			for i, v := range vectors {
				if len(v) != embedDim {
					t.Fatalf("vectors[%d] dim = %d, want %d", i, len(v), embedDim)
				}
			}

			for i, v := range vectors {
				doc := vector.Document{
					ID:        fmt.Sprintf("doc-%d", i),
					Text:      texts[i],
					Embedding: v,
					Metadata:  map[string]any{"topic": fmt.Sprintf("t%d", i)},
				}
				if err := store.Add(ctx, doc); err != nil {
					t.Fatalf("Add %d: %v", i, err)
				}
			}

			hits, err := store.Search(ctx, vectors[0], vector.SearchOptions{TopK: 4})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) != 4 {
				t.Errorf("hits len = %d, want 4", len(hits))
			}
			// Self-embedding must rank its own document first — this is
			// what proves BatchEmbed kept results aligned with inputs.
			if hits[0].Document.ID != "doc-0" {
				t.Errorf("top hit on self-embedding = %q, want doc-0", hits[0].Document.ID)
			}
		})
	}
}

// TestDimensionAlignment_FirstAddVsLocked validates that the two ways
// of pinning dimension ("first Add" vs "WithLockedDimension") produce
// equivalent observable behaviour, for every provider. This is the §4.9
// alignment-strategy concern in the design doc, and it is what keeps a
// provider swap from silently corrupting a collection.
func TestDimensionAlignment_FirstAddVsLocked(t *testing.T) {
	url := skipUnlessQdrant(t)

	for _, pc := range providerMatrix() {
		t.Run(pc.name, func(t *testing.T) {
			emb := pc.newEmbedder(t, embedDim)

			ctx := context.Background()
			colA := uniqueCollection("dimA_firstadd_" + pc.name)
			colB := uniqueCollection("dimB_locked_" + pc.name)

			storeA, err := qdrant.New(url, colA)
			if err != nil {
				t.Fatalf("qdrant.New A: %v", err)
			}
			storeB, err := qdrant.New(url, colB, qdrant.WithLockedDimension(embedDim))
			if err != nil {
				t.Fatalf("qdrant.New B: %v", err)
			}
			t.Cleanup(func() { dropCollection(t, url, colA) })
			t.Cleanup(func() { dropCollection(t, url, colB) })

			v, err := emb.Embed(ctx, "alignment seed text")
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}
			doc := vector.Document{ID: "seed", Text: "alignment seed text", Embedding: v}

			if err := storeA.Add(ctx, doc); err != nil {
				t.Fatalf("storeA Add: %v", err)
			}
			if err := storeB.Add(ctx, doc); err != nil {
				t.Fatalf("storeB Add: %v", err)
			}
			if storeA.LockedDimension() != storeB.LockedDimension() {
				t.Errorf("dim mismatch between paths: A=%d B=%d", storeA.LockedDimension(), storeB.LockedDimension())
			}

			// Both paths must reject mismatched dim with the SAME sentinel.
			bad := vector.Document{ID: "bad", Embedding: make([]float32, 64)}
			errA := storeA.Add(ctx, bad)
			errB := storeB.Add(ctx, bad)
			if !errors.Is(errA, vector.ErrDimensionMismatch) {
				t.Errorf("storeA: expected ErrDimensionMismatch, got %v", errA)
			}
			if !errors.Is(errB, vector.ErrDimensionMismatch) {
				t.Errorf("storeB: expected ErrDimensionMismatch, got %v", errB)
			}
		})
	}
}

// TestMetadataFilter_PushedDown verifies that a string-equality filter
// on payload narrows results server-side — the dominant push-down case
// covered by buildFilter — independently of which provider produced the
// vectors.
func TestMetadataFilter_PushedDown(t *testing.T) {
	url := skipUnlessQdrant(t)

	for _, pc := range providerMatrix() {
		t.Run(pc.name, func(t *testing.T) {
			emb := pc.newEmbedder(t, embedDim)

			ctx := context.Background()
			col := uniqueCollection("filter_" + pc.name)

			store, err := qdrant.New(url, col)
			if err != nil {
				t.Fatalf("qdrant.New: %v", err)
			}
			t.Cleanup(func() { dropCollection(t, url, col) })

			type item struct {
				id, text, cat string
			}
			items := []item{
				{"a1", "machine learning fundamentals", "ml"},
				{"a2", "deep learning architectures", "ml"},
				{"b1", "spaghetti carbonara recipe", "food"},
				{"b2", "italian pasta dishes", "food"},
			}
			for _, it := range items {
				v, err := emb.Embed(ctx, it.text)
				if err != nil {
					t.Fatalf("Embed %s: %v", it.id, err)
				}
				if err := store.Add(ctx, vector.Document{
					ID: it.id, Text: it.text, Embedding: v,
					Metadata: map[string]any{"category": it.cat},
				}); err != nil {
					t.Fatalf("Add %s: %v", it.id, err)
				}
			}

			q, err := emb.Embed(ctx, "neural networks")
			if err != nil {
				t.Fatalf("query Embed: %v", err)
			}
			hits, err := store.Search(ctx, q, vector.SearchOptions{
				TopK:           4,
				MetadataEquals: map[string]any{"category": "ml"},
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) == 0 {
				t.Fatal("Search returned no hits with filter")
			}
			for _, h := range hits {
				if cat, _ := h.Document.Metadata["category"].(string); cat != "ml" {
					t.Errorf("filter leaked: hit %q has category=%q", h.Document.ID, cat)
				}
			}
		})
	}
}

// dropCollection removes every collection point via the integration
// store's List + Delete loop. We deliberately avoid wrapping
// `DELETE /collections/{name}` because it is out of scope for the
// vector.VectorStore contract; cleaning by ID keeps the package
// surface minimal while still leaving the qdrant instance tidy.
//
// On error we log instead of failing the test — cleanup is best-effort.
func dropCollection(t *testing.T, baseURL, col string) {
	t.Helper()
	url := baseURL + "/collections/" + col
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Logf("cleanup: build request: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("cleanup: do request: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
}

func briefHits(hits []vector.SearchHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = fmt.Sprintf("%s(%.3f)", h.Document.ID, h.Score)
	}
	return out
}
