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

package vector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func mustEmbed(t *testing.T, h Embedder, text string) []float32 {
	t.Helper()
	v, err := h.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed %q: %v", text, err)
	}
	return v
}

func addDoc(t *testing.T, s VectorStore, h Embedder, id, text string, md map[string]any) {
	t.Helper()
	doc := Document{
		ID:        id,
		Text:      text,
		Embedding: mustEmbed(t, h, text),
		Metadata:  md,
	}
	if err := s.Add(context.Background(), doc); err != nil {
		t.Fatalf("add %s: %v", id, err)
	}
}

func TestMapVectorStore_AddSearch(t *testing.T) {
	h := NewHashEmbedder(128)
	s := NewMapVectorStore()

	addDoc(t, s, h, "fox", "the quick brown fox jumps over the lazy dog", nil)
	addDoc(t, s, h, "cooking", "boil pasta then drain and serve", nil)
	addDoc(t, s, h, "vehicles", "the car drove past the brown gate", nil)

	q := mustEmbed(t, h, "brown fox jumping")
	hits, err := s.Search(context.Background(), q, SearchOptions{TopK: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected hits, got none")
	}
	if hits[0].Document.ID != "fox" {
		t.Fatalf("top hit = %q, want fox; full = %+v", hits[0].Document.ID, hits)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Score < hits[i].Score {
			t.Fatalf("hits not sorted desc by score: %+v", hits)
		}
	}
}

func TestMapVectorStore_DimensionLock_OnAdd(t *testing.T) {
	s := NewMapVectorStore()
	if err := s.Add(context.Background(), Document{ID: "a", Embedding: []float32{1, 2, 3}}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := s.Add(context.Background(), Document{ID: "b", Embedding: []float32{1, 2, 3, 4}})
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
	if got := s.LockedDimension(); got != 3 {
		t.Fatalf("locked dim = %d, want 3", got)
	}
}

func TestMapVectorStore_DimensionLock_Explicit(t *testing.T) {
	s := NewMapVectorStore(WithLockedDimension(4))
	if got := s.LockedDimension(); got != 4 {
		t.Fatalf("locked dim = %d, want 4", got)
	}
	err := s.Add(context.Background(), Document{ID: "a", Embedding: []float32{1, 2}})
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
}

func TestMapVectorStore_Search_DimensionMismatch(t *testing.T) {
	s := NewMapVectorStore(WithLockedDimension(3))
	_, err := s.Search(context.Background(), []float32{1, 2, 3, 4}, SearchOptions{})
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("expected ErrDimensionMismatch, got %v", err)
	}
}

func TestMapVectorStore_EmptyEmbeddingOnAdd(t *testing.T) {
	s := NewMapVectorStore()
	err := s.Add(context.Background(), Document{ID: "a"})
	if !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestMapVectorStore_EmptyQueryOnSearch(t *testing.T) {
	s := NewMapVectorStore()
	_, err := s.Search(context.Background(), nil, SearchOptions{})
	if !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestMapVectorStore_TopK(t *testing.T) {
	h := NewHashEmbedder(64)
	s := NewMapVectorStore(WithDefaultTopK(2))
	addDoc(t, s, h, "a", "alpha", nil)
	addDoc(t, s, h, "b", "alpha beta", nil)
	addDoc(t, s, h, "c", "alpha beta gamma", nil)

	q := mustEmbed(t, h, "alpha")
	hits, _ := s.Search(context.Background(), q, SearchOptions{})
	if len(hits) != 2 {
		t.Fatalf("default TopK=2 should cap to 2, got %d", len(hits))
	}

	hits, _ = s.Search(context.Background(), q, SearchOptions{TopK: 5})
	if len(hits) != 3 {
		t.Fatalf("explicit TopK=5 should return all 3, got %d", len(hits))
	}
}

func TestMapVectorStore_MinScore(t *testing.T) {
	h := NewHashEmbedder(64)
	s := NewMapVectorStore()
	addDoc(t, s, h, "a", "alpha beta", nil)
	addDoc(t, s, h, "b", "completely different", nil)

	q := mustEmbed(t, h, "alpha")
	hits, _ := s.Search(context.Background(), q, SearchOptions{MinScore: 0.5})
	for _, hit := range hits {
		if hit.Score < 0.5 {
			t.Fatalf("hit %q has score %v < 0.5", hit.Document.ID, hit.Score)
		}
	}
	// "completely different" has zero shared tokens -> score 0 -> filtered.
	for _, hit := range hits {
		if hit.Document.ID == "b" {
			t.Fatalf("low-score doc b leaked through MinScore")
		}
	}
}

func TestMapVectorStore_MetadataEquals(t *testing.T) {
	h := NewHashEmbedder(64)
	s := NewMapVectorStore()
	addDoc(t, s, h, "a", "the brown fox", map[string]any{"kind": "fact", "lang": "en"})
	addDoc(t, s, h, "b", "the brown fox", map[string]any{"kind": "note", "lang": "en"})
	addDoc(t, s, h, "c", "the brown fox", map[string]any{"kind": "fact", "lang": "fr"})

	q := mustEmbed(t, h, "brown fox")
	hits, _ := s.Search(context.Background(), q, SearchOptions{
		MetadataEquals: map[string]any{"kind": "fact", "lang": "en"},
	})
	if len(hits) != 1 || hits[0].Document.ID != "a" {
		t.Fatalf("expected only a; got %+v", hits)
	}
}

func TestMapVectorStore_Predicate(t *testing.T) {
	h := NewHashEmbedder(64)
	s := NewMapVectorStore()
	addDoc(t, s, h, "x", "alpha", map[string]any{"score": 0.9})
	addDoc(t, s, h, "y", "alpha", map[string]any{"score": 0.1})

	q := mustEmbed(t, h, "alpha")
	hits, _ := s.Search(context.Background(), q, SearchOptions{
		Predicate: func(d Document) bool {
			s, _ := d.Metadata["score"].(float64)
			return s >= 0.5
		},
	})
	if len(hits) != 1 || hits[0].Document.ID != "x" {
		t.Fatalf("expected only x; got %+v", hits)
	}
}

func TestMapVectorStore_Delete_MissingIsSilent(t *testing.T) {
	s := NewMapVectorStore()
	if err := s.Delete(context.Background(), "nonexistent"); err != nil {
		t.Fatalf("delete missing should be silent, got %v", err)
	}
}

func TestMapVectorStore_Delete_RemovesFromSearch(t *testing.T) {
	h := NewHashEmbedder(64)
	s := NewMapVectorStore()
	addDoc(t, s, h, "a", "alpha", nil)

	if err := s.Delete(context.Background(), "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, _ := s.Search(context.Background(), mustEmbed(t, h, "alpha"), SearchOptions{})
	if len(hits) != 0 {
		t.Fatalf("expected empty hits after delete, got %+v", hits)
	}
}

func TestMapVectorStore_List(t *testing.T) {
	h := NewHashEmbedder(32)
	s := NewMapVectorStore()
	addDoc(t, s, h, "a", "one", nil)
	addDoc(t, s, h, "b", "two", nil)

	docs, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len = %d, want 2", len(docs))
	}
}

func TestMapVectorStore_ZeroVectorCosine_NoNaN(t *testing.T) {
	s := NewMapVectorStore()
	zero := []float32{0, 0, 0, 0}
	one := []float32{1, 0, 0, 0}

	if err := s.Add(context.Background(), Document{ID: "a", Embedding: one}); err != nil {
		t.Fatalf("add: %v", err)
	}
	hits, err := s.Search(context.Background(), zero, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, h := range hits {
		// math.IsNaN takes float64; promote.
		if isNaN32(h.Score) {
			t.Fatalf("got NaN score: %+v", h)
		}
	}
}

func TestMapVectorStore_DefensiveCopy(t *testing.T) {
	s := NewMapVectorStore()
	emb := []float32{0.1, 0.2, 0.3}
	if err := s.Add(context.Background(), Document{ID: "a", Embedding: emb}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Mutate the caller's slice — store must not see this change.
	emb[0] = 99

	docs, _ := s.List(context.Background())
	if got := docs[0].Embedding[0]; got == 99 {
		t.Fatalf("store reflected caller mutation; embeddings not copied")
	}
}

func TestMapVectorStore_DefensiveCopy_Metadata(t *testing.T) {
	s := NewMapVectorStore()
	md := map[string]any{"kind": "fact"}
	emb := []float32{0.1, 0.2, 0.3}
	if err := s.Add(context.Background(), Document{ID: "a", Embedding: emb, Metadata: md}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Mutate the caller's map — store must not see this change.
	md["kind"] = "tampered"
	delete(md, "kind")

	docs, _ := s.List(context.Background())
	if got, _ := docs[0].Metadata["kind"].(string); got != "fact" {
		t.Fatalf("store reflected caller metadata mutation: kind=%q", got)
	}

	// Mutating the listed copy must not affect the store either.
	docs[0].Metadata["kind"] = "tampered2"
	docs[0].Embedding[0] = 99
	docs2, _ := s.List(context.Background())
	if got, _ := docs2[0].Metadata["kind"].(string); got != "fact" {
		t.Fatalf("store reflected listed-doc metadata mutation: kind=%q", got)
	}
	if got := docs2[0].Embedding[0]; got == 99 {
		t.Fatalf("store reflected listed-doc embedding mutation")
	}
}

func TestMapVectorStore_Search_DefensiveCopy(t *testing.T) {
	h := NewHashEmbedder(32)
	s := NewMapVectorStore()
	addDoc(t, s, h, "a", "alpha beta", map[string]any{"kind": "fact"})

	q := mustEmbed(t, h, "alpha")
	hits, err := s.Search(context.Background(), q, SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected hits")
	}
	hits[0].Document.Metadata["kind"] = "tampered"
	hits[0].Document.Embedding[0] = 99

	docs, _ := s.List(context.Background())
	for _, d := range docs {
		if d.ID != "a" {
			continue
		}
		if got, _ := d.Metadata["kind"].(string); got != "fact" {
			t.Fatalf("store reflected search-result metadata mutation: kind=%q", got)
		}
		if d.Embedding[0] == 99 {
			t.Fatalf("store reflected search-result embedding mutation")
		}
	}
}

func TestMapVectorStore_CtxCancellation(t *testing.T) {
	s := NewMapVectorStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Add(ctx, Document{ID: "a", Embedding: []float32{1, 2}}); !errors.Is(err, context.Canceled) {
		t.Errorf("Add: want context.Canceled, got %v", err)
	}
	if _, err := s.Search(ctx, []float32{1, 2}, SearchOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Search: want context.Canceled, got %v", err)
	}
	if err := s.Delete(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete: want context.Canceled, got %v", err)
	}
	if _, err := s.List(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("List: want context.Canceled, got %v", err)
	}
}

func TestMapVectorStore_ConcurrentAddSearch(t *testing.T) {
	h := NewHashEmbedder(32)
	s := NewMapVectorStore()
	addDoc(t, s, h, "seed", "alpha beta", nil)

	var wg sync.WaitGroup
	// 4 writers
	for i := range 4 {
		wg.Go(func() {
			for j := range 25 {
				doc := Document{
					ID:        fmt.Sprintf("w%d-%d", i, j),
					Text:      "alpha beta",
					Embedding: mustEmbed(t, h, "alpha beta"),
				}
				if err := s.Add(context.Background(), doc); err != nil {
					t.Errorf("add: %v", err)
					return
				}
			}
		})
	}
	// 8 readers
	for range 8 {
		wg.Go(func() {
			q := mustEmbed(t, h, "alpha")
			for range 50 {
				if _, err := s.Search(context.Background(), q, SearchOptions{}); err != nil {
					t.Errorf("search: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestMapVectorStore_Concurrent(t *testing.T) {
	h := NewHashEmbedder(32)
	s := NewMapVectorStore()
	addDoc(t, s, h, "seed", "alpha beta", nil)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			q := mustEmbed(t, h, "alpha")
			for range 50 {
				_, err := s.Search(context.Background(), q, SearchOptions{})
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// isNaN32 promotes a float32 to float64 to use math.IsNaN.
func isNaN32(f float32) bool { return f != f }
