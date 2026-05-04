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

package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/vector"
)

// fakeQdrant captures what the Store sent and returns canned responses.
// Using a single-server-with-Mux fixture keeps each test independent
// without a global state singleton.
type fakeQdrant struct {
	t          *testing.T
	srv        *httptest.Server
	collection string

	// recorded
	createCalls      atomic.Int32
	upsertCalls      atomic.Int32
	searchCalls      atomic.Int32
	deleteCalls      atomic.Int32
	scrollCalls      atomic.Int32
	lastUpsertVector []float32
	lastSearchReq    *searchRequest
	lastDeleteReq    *deletePointsRequest

	// configurables
	createStatus      int    // 0 -> 200
	createBody        string // verbatim body when createStatus != 0
	collectionExists  bool   // when true, create returns 409 already-exists
	upsertStatus      int    // 0 -> 200
	upsertBody        string // verbatim body when upsertStatus != 0
	searchHits        []scoredPoint
	deleteStatus      int
	deleteBody        string
	scrollPages       []scrollResponse // returned in order
	scrollPageIdx     int
	expectAuthAPIKey  string // when set, asserts api-key header
	expectContentType bool
	dimensionMismatch bool
}

func newFake(t *testing.T) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{t: t, collection: "test_col"}
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/test_col", f.handleCollectionRoot)
	mux.HandleFunc("/collections/test_col/points", f.handleUpsert)
	mux.HandleFunc("/collections/test_col/points/search", f.handleSearch)
	mux.HandleFunc("/collections/test_col/points/delete", f.handleDelete)
	mux.HandleFunc("/collections/test_col/points/scroll", f.handleScroll)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeQdrant) close() { f.srv.Close() }

func (f *fakeQdrant) store(opts ...Option) *Store {
	f.t.Helper()
	s, err := New(f.srv.URL, f.collection, opts...)
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	return s
}

func (f *fakeQdrant) handleCollectionRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		return
	}
	f.assertAuth(r)
	f.createCalls.Add(1)
	if f.collectionExists {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"status":{"error":"Collection `+f.collection+` already exists!"}}`)
		return
	}
	if f.createStatus != 0 {
		w.WriteHeader(f.createStatus)
		if f.createBody != "" {
			_, _ = io.WriteString(w, f.createBody)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(genericResponse{Status: "ok"})
}

func (f *fakeQdrant) handleUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		return
	}
	f.assertAuth(r)
	f.upsertCalls.Add(1)
	var body upsertPointsRequest
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Points) > 0 {
		f.lastUpsertVector = body.Points[0].Vector
	}
	if f.dimensionMismatch {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"status":{"error":"Wrong vector size"}}`)
		return
	}
	if f.upsertStatus != 0 {
		w.WriteHeader(f.upsertStatus)
		if f.upsertBody != "" {
			_, _ = io.WriteString(w, f.upsertBody)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(genericResponse{Status: "ok"})
}

func (f *fakeQdrant) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		return
	}
	f.assertAuth(r)
	f.searchCalls.Add(1)
	var body searchRequest
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.lastSearchReq = &body
	resp := searchResponse{Status: "ok", Result: f.searchHits}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeQdrant) handleDelete(w http.ResponseWriter, r *http.Request) {
	f.assertAuth(r)
	f.deleteCalls.Add(1)
	var body deletePointsRequest
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.lastDeleteReq = &body
	if f.deleteStatus != 0 {
		w.WriteHeader(f.deleteStatus)
		if f.deleteBody != "" {
			_, _ = io.WriteString(w, f.deleteBody)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(genericResponse{Status: "ok"})
}

func (f *fakeQdrant) handleScroll(w http.ResponseWriter, _ *http.Request) {
	f.scrollCalls.Add(1)
	if f.scrollPageIdx >= len(f.scrollPages) {
		_ = json.NewEncoder(w).Encode(scrollResponse{})
		return
	}
	resp := f.scrollPages[f.scrollPageIdx]
	f.scrollPageIdx++
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeQdrant) assertAuth(r *http.Request) {
	if f.expectAuthAPIKey != "" {
		if got := r.Header.Get("api-key"); got != f.expectAuthAPIKey {
			f.t.Errorf("api-key = %q, want %q", got, f.expectAuthAPIKey)
		}
	}
	if f.expectContentType && r.Method != http.MethodGet {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			f.t.Errorf("Content-Type = %q, want application/json", got)
		}
	}
}

func TestNew_Validates(t *testing.T) {
	if _, err := New("", "c"); err == nil {
		t.Error("expected error for empty baseURL")
	}
	if _, err := New("http://x", ""); err == nil {
		t.Error("expected error for empty collection")
	}
}

func TestAdd_FirstAddCreatesCollection_AndLocksDim(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store()

	doc := vector.Document{ID: "doc-1", Embedding: []float32{0.1, 0.2, 0.3}, Text: "hello"}
	if err := s.Add(context.Background(), doc); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if f.createCalls.Load() != 1 {
		t.Errorf("createCalls = %d, want 1", f.createCalls.Load())
	}
	if f.upsertCalls.Load() != 1 {
		t.Errorf("upsertCalls = %d, want 1", f.upsertCalls.Load())
	}
	if got := s.LockedDimension(); got != 3 {
		t.Errorf("LockedDimension = %d, want 3", got)
	}
	if !equalFloat32(f.lastUpsertVector, doc.Embedding) {
		t.Errorf("upsert vector = %v, want %v", f.lastUpsertVector, doc.Embedding)
	}
}

func TestAdd_DimensionMismatch_LocalCheck(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store()

	if err := s.Add(context.Background(), vector.Document{ID: "a", Embedding: []float32{1, 2, 3}}); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	err := s.Add(context.Background(), vector.Document{ID: "b", Embedding: []float32{1, 2, 3, 4}})
	if !errors.Is(err, vector.ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
	// Second Add must NOT have hit upsert (we caught before request).
	if f.upsertCalls.Load() != 1 {
		t.Errorf("upsertCalls = %d, want 1", f.upsertCalls.Load())
	}
}

func TestAdd_EmptyEmbedding(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store()

	err := s.Add(context.Background(), vector.Document{ID: "x", Embedding: nil})
	if !errors.Is(err, vector.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestAdd_CollectionAlreadyExists_TreatedAsSuccess(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.collectionExists = true
	s := f.store()

	if err := s.Add(context.Background(), vector.Document{ID: "x", Embedding: []float32{1, 2}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Despite the 409, ensure must have been marked done so the next
	// Add does not retry create.
	if err := s.Add(context.Background(), vector.Document{ID: "y", Embedding: []float32{3, 4}}); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if f.createCalls.Load() != 1 {
		t.Errorf("createCalls = %d, want 1 (cached)", f.createCalls.Load())
	}
}

func TestAdd_DimensionMismatch_FromServerBody(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.dimensionMismatch = true
	s := f.store()

	err := s.Add(context.Background(), vector.Document{ID: "x", Embedding: []float32{1, 2}})
	if !errors.Is(err, vector.ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch from server, got %v", err)
	}
}

func TestWithLockedDimension_EagerCreate(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store(WithLockedDimension(384))

	if got := s.LockedDimension(); got != 384 {
		t.Fatalf("LockedDimension = %d, want 384", got)
	}
	// Add with the locked dim should succeed and create collection
	// with size=384 (we cannot inspect without exposing the request,
	// but the LockedDimension assertion above is sufficient evidence).
	if err := s.Add(context.Background(), vector.Document{ID: "x", Embedding: make([]float32, 384)}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// And mismatch:
	err := s.Add(context.Background(), vector.Document{ID: "y", Embedding: make([]float32, 256)})
	if !errors.Is(err, vector.ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
}

func TestSearch_SendsCorrectRequest(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.searchHits = []scoredPoint{
		{
			ID:    "uuid-1",
			Score: 0.9,
			Payload: map[string]any{
				payloadKeyVageID: "doc-1",
				payloadKeyText:   "first",
				"category":       "a",
			},
		},
		{
			ID:    "uuid-2",
			Score: 0.7,
			Payload: map[string]any{
				payloadKeyVageID: "doc-2",
				payloadKeyText:   "second",
				"category":       "b",
			},
		},
	}
	s := f.store()
	// Pre-lock dim so Search dimension assertion fires.
	if err := s.Add(context.Background(), vector.Document{ID: "seed", Embedding: []float32{0, 0, 0}}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	hits, err := s.Search(context.Background(), []float32{1, 0, 0}, vector.SearchOptions{
		TopK:           2,
		MinScore:       0.5,
		MetadataEquals: map[string]any{"category": "a"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if f.lastSearchReq == nil {
		t.Fatal("search request was not captured")
	}
	if f.lastSearchReq.Limit != 2 {
		t.Errorf("Limit = %d, want 2", f.lastSearchReq.Limit)
	}
	if f.lastSearchReq.Filter == nil || len(f.lastSearchReq.Filter.Must) != 1 {
		t.Errorf("filter not pushed down: %+v", f.lastSearchReq.Filter)
	}
	if f.lastSearchReq.ScoreThresh == nil || *f.lastSearchReq.ScoreThresh != 0.5 {
		t.Errorf("ScoreThresh = %v", f.lastSearchReq.ScoreThresh)
	}
	if len(hits) != 2 {
		t.Errorf("len(hits) = %d, want 2", len(hits))
	}
	// Server-side filtering is faked — we just assert decode.
	if hits[0].Document.ID != "doc-1" || hits[0].Document.Text != "first" {
		t.Errorf("hit 0 decode wrong: %+v", hits[0].Document)
	}
	if cat, ok := hits[0].Document.Metadata["category"]; !ok || cat != "a" {
		t.Errorf("metadata leak: %+v", hits[0].Document.Metadata)
	}
	// Reserved keys must NOT leak into Metadata.
	if _, ok := hits[0].Document.Metadata[payloadKeyVageID]; ok {
		t.Errorf("reserved _vage_id leaked into Metadata")
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store()
	_, err := s.Search(context.Background(), nil, vector.SearchOptions{})
	if !errors.Is(err, vector.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestSearch_DimensionMismatch_LocalCheck(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store(WithLockedDimension(4))
	_, err := s.Search(context.Background(), []float32{1, 2, 3}, vector.SearchOptions{})
	if !errors.Is(err, vector.ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
}

func TestSearch_ClientSidePredicateFilters(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.searchHits = []scoredPoint{
		{ID: "u1", Score: 0.9, Payload: map[string]any{payloadKeyVageID: "d1"}},
		{ID: "u2", Score: 0.8, Payload: map[string]any{payloadKeyVageID: "d2"}},
	}
	s := f.store()
	_ = s.Add(context.Background(), vector.Document{ID: "seed", Embedding: []float32{0, 0}})

	hits, err := s.Search(context.Background(), []float32{1, 0}, vector.SearchOptions{
		Predicate: func(d vector.Document) bool { return d.ID == "d2" },
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Document.ID != "d2" {
		t.Fatalf("predicate not applied: %+v", hits)
	}
}

func TestSearch_UnpushableMetadataAppliedClientSide(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.searchHits = []scoredPoint{
		{ID: "u1", Score: 0.9, Payload: map[string]any{payloadKeyVageID: "d1", "tags": []any{"a", "b"}}},
		{ID: "u2", Score: 0.8, Payload: map[string]any{payloadKeyVageID: "d2", "tags": []any{"c"}}},
	}
	s := f.store()
	_ = s.Add(context.Background(), vector.Document{ID: "seed", Embedding: []float32{0, 0}})

	hits, err := s.Search(context.Background(), []float32{1, 0}, vector.SearchOptions{
		MetadataEquals: map[string]any{"tags": []any{"a", "b"}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Server filter should NOT have been built (slice value), so both
	// hits come back from the fake; the client-side filter narrows.
	if f.lastSearchReq.Filter != nil {
		t.Errorf("expected no pushdown filter for slice value, got %+v", f.lastSearchReq.Filter)
	}
	if len(hits) != 1 || hits[0].Document.ID != "d1" {
		t.Fatalf("expected only d1 to survive, got %+v", hits)
	}
}

func TestDelete_HappyPath(t *testing.T) {
	f := newFake(t)
	defer f.close()
	s := f.store()

	if err := s.Delete(context.Background(), "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.deleteCalls.Load() != 1 {
		t.Errorf("deleteCalls = %d", f.deleteCalls.Load())
	}
	if f.lastDeleteReq == nil || len(f.lastDeleteReq.Points) != 1 {
		t.Fatalf("lastDeleteReq = %+v", f.lastDeleteReq)
	}
	// Point ID must be the derived UUID, not the user string.
	if f.lastDeleteReq.Points[0] == "doc-1" {
		t.Errorf("point ID was not derived: %q", f.lastDeleteReq.Points[0])
	}
}

func TestDelete_CollectionMissing_IsSilent(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.deleteStatus = http.StatusNotFound
	f.deleteBody = `{"status":{"error":"Collection not found"}}`
	s := f.store()

	if err := s.Delete(context.Background(), "doc-1"); err != nil {
		t.Fatalf("Delete should be silent on missing collection, got %v", err)
	}
}

func TestList_Pagination(t *testing.T) {
	f := newFake(t)
	defer f.close()
	page1Off := "next-1"
	f.scrollPages = []scrollResponse{
		{Result: struct {
			Points         []scoredPoint `json:"points"`
			NextPageOffset *string       `json:"next_page_offset"`
		}{
			Points: []scoredPoint{
				{ID: "u1", Payload: map[string]any{payloadKeyVageID: "d1", payloadKeyText: "t1"}, Vector: []float32{1, 0}},
			},
			NextPageOffset: &page1Off,
		}},
		{Result: struct {
			Points         []scoredPoint `json:"points"`
			NextPageOffset *string       `json:"next_page_offset"`
		}{
			Points: []scoredPoint{
				{ID: "u2", Payload: map[string]any{payloadKeyVageID: "d2", payloadKeyText: "t2"}, Vector: []float32{0, 1}},
			},
			NextPageOffset: nil,
		}},
	}
	s := f.store()

	docs, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len(docs) = %d, want 2", len(docs))
	}
	if f.scrollCalls.Load() != 2 {
		t.Errorf("scrollCalls = %d, want 2", f.scrollCalls.Load())
	}
	if docs[0].ID != "d1" || docs[1].ID != "d2" {
		t.Errorf("decoded IDs = %q, %q", docs[0].ID, docs[1].ID)
	}
}

func TestAPIKey_HeaderSent(t *testing.T) {
	f := newFake(t)
	defer f.close()
	f.expectAuthAPIKey = "secret"
	s := f.store(WithAPIKey("secret"))

	if err := s.Add(context.Background(), vector.Document{ID: "x", Embedding: []float32{1, 2}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func TestUUIDV5_Deterministic(t *testing.T) {
	a := derivePointID("doc-1")
	b := derivePointID("doc-1")
	c := derivePointID("doc-2")
	if a != b {
		t.Errorf("expected determinism: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("expected uniqueness: %q == %q", a, c)
	}
	// 8-4-4-4-12 hex format
	if len(strings.Split(a, "-")) != 5 {
		t.Errorf("UUID format: %q", a)
	}
}

func TestBuildFilter_MixedTypes(t *testing.T) {
	got := buildFilter(map[string]any{
		"category": "a",
		"verified": true,
		"version":  42,
		"tags":     []string{"x"}, // unpushable, must be skipped
		"weight":   1.5,
	})
	if got == nil {
		t.Fatal("nil filter")
	}
	// 4 pushable types, 1 skipped slice.
	if len(got.Must) != 4 {
		t.Errorf("len(must) = %d, want 4", len(got.Must))
	}
	for _, c := range got.Must {
		if c.Key == "tags" {
			t.Errorf("tags should not have been pushed: %+v", c)
		}
	}
}

func TestBuildFilter_AllUnpushable_ReturnsNil(t *testing.T) {
	got := buildFilter(map[string]any{
		"slice": []int{1, 2},
		"map":   map[string]int{"a": 1},
	})
	if got != nil {
		t.Errorf("expected nil filter, got %+v", got)
	}
}

func equalFloat32(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
