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
	"maps"
	"math"
	"reflect"
	"sort"
	"sync"
	"time"
)

// DefaultTopK is the TopK MapVectorStore.Search uses when SearchOptions.TopK
// is non-positive. Real backends should document their own default.
const DefaultTopK = 5

// MapVectorStore is an in-process VectorStore backed by a plain map.
// It uses cosine similarity (`dot(a,b) / (norm(a) * norm(b))`) and a
// brute-force linear scan, so it is intended for tests, local
// experimentation, and small fixture sets.
//
// Performance footgun: lookups are O(N * dim). Past a few thousand
// documents the latency becomes user-visible; production paths should
// implement VectorStore against a real ANN backend.
type MapVectorStore struct {
	mu          sync.RWMutex
	docs        map[string]Document
	defaultTopK int
	lockedDim   int  // 0 -> unlocked; will be set on first Add
	dimExplicit bool // true -> set via WithLockedDimension; do not relock
}

// MapStoreOption configures a MapVectorStore.
type MapStoreOption func(*MapVectorStore)

// WithDefaultTopK overrides the default TopK used when SearchOptions.TopK
// is unset.
func WithDefaultTopK(k int) MapStoreOption {
	return func(s *MapVectorStore) {
		if k > 0 {
			s.defaultTopK = k
		}
	}
}

// WithLockedDimension locks the embedding dimension at construction
// time, skipping the first-Add lock. Useful when wiring a store before
// any documents are inserted.
func WithLockedDimension(d int) MapStoreOption {
	return func(s *MapVectorStore) {
		if d > 0 {
			s.lockedDim = d
			s.dimExplicit = true
		}
	}
}

// Compile-time check.
var _ VectorStore = (*MapVectorStore)(nil)

// NewMapVectorStore constructs a MapVectorStore with the given options.
func NewMapVectorStore(opts ...MapStoreOption) *MapVectorStore {
	s := &MapVectorStore{
		docs:        make(map[string]Document),
		defaultTopK: DefaultTopK,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Add inserts or replaces a document. The first Add locks the embedding
// dimension; subsequent Adds with mismatched length return
// ErrDimensionMismatch. CreatedAt is stamped to time.Now() when zero.
func (s *MapVectorStore) Add(ctx context.Context, doc Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lockedDim == 0 {
		if len(doc.Embedding) == 0 {
			return ErrEmptyQuery
		}
		s.lockedDim = len(doc.Embedding)
	} else if len(doc.Embedding) != s.lockedDim {
		return ErrDimensionMismatch
	}

	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}

	// Defensive copy of the embedding and metadata so callers cannot
	// mutate stored data after the call returns. The metadata map is
	// shallow-copied — values that are themselves mutable (slices, maps)
	// remain aliased; that is by design (the typical metadata payload is
	// scalar, and a deep copy of arbitrary `any` values is expensive).
	doc.Embedding = cloneFloats(doc.Embedding)
	doc.Metadata = cloneMetadata(doc.Metadata)

	s.docs[doc.ID] = doc
	return nil
}

// cloneFloats returns an independent copy of v. Returns nil for nil to
// preserve zero-value semantics.
func cloneFloats(v []float32) []float32 {
	if v == nil {
		return nil
	}
	out := make([]float32, len(v))
	copy(out, v)
	return out
}

// cloneMetadata returns a shallow copy of m. Returns nil for nil to
// preserve zero-value semantics.
func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

// Search returns up to TopK hits ordered by descending cosine similarity.
//
// Filters are applied in this order: MetadataEquals (declarative,
// equality), Predicate (client-side closure). Both are applied BEFORE
// truncating to TopK so that filters reduce candidate count.
func (s *MapVectorStore) Search(ctx context.Context, query []float32, opts SearchOptions) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query) == 0 {
		return nil, ErrEmptyQuery
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.lockedDim != 0 && len(query) != s.lockedDim {
		return nil, ErrDimensionMismatch
	}

	queryNorm := vectorNorm(query)

	hits := make([]SearchHit, 0, len(s.docs))
	for _, doc := range s.docs {
		if !matchMetadataEquals(doc.Metadata, opts.MetadataEquals) {
			continue
		}
		if opts.Predicate != nil && !opts.Predicate(doc) {
			continue
		}

		score := cosine(query, queryNorm, doc.Embedding)
		if opts.MinScore != 0 && score < opts.MinScore {
			continue
		}

		hits = append(hits, SearchHit{Document: doc, Score: score})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Score > hits[j].Score
	})

	topK := opts.TopK
	if topK <= 0 {
		topK = s.defaultTopK
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	// Defensively clone surviving hits so callers cannot mutate stored
	// data through Embedding/Metadata aliases. We clone after TopK so we
	// only pay for documents the caller actually receives.
	for i := range hits {
		hits[i].Document.Embedding = cloneFloats(hits[i].Document.Embedding)
		hits[i].Document.Metadata = cloneMetadata(hits[i].Document.Metadata)
	}
	return hits, nil
}

// Delete removes the document with the given ID. Missing IDs are silent
// successes (parity with memory.MapStore).
func (s *MapVectorStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, id)
	return nil
}

// List returns every stored document. Order is unspecified. Returned
// documents are defensively cloned (Embedding slice + Metadata map) so
// callers can mutate them without corrupting the store — important
// because MapVectorStore is documented as concurrent-safe.
func (s *MapVectorStore) List(ctx context.Context) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Document, 0, len(s.docs))
	for _, d := range s.docs {
		d.Embedding = cloneFloats(d.Embedding)
		d.Metadata = cloneMetadata(d.Metadata)
		out = append(out, d)
	}
	return out, nil
}

// Len returns the number of stored documents. Useful in tests.
func (s *MapVectorStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}

// LockedDimension returns the dimension the store is locked to, or 0 when
// no Add has yet occurred (and no explicit option was set).
func (s *MapVectorStore) LockedDimension() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lockedDim
}

// vectorNorm returns the Euclidean norm of v. Returns 0 for zero vectors
// so cosine can short-circuit to 0 instead of NaN.
func vectorNorm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// cosine computes cosine similarity between query (with precomputed norm)
// and target. Returns 0 when either input is a zero vector.
func cosine(query []float32, queryNorm float64, target []float32) float32 {
	if queryNorm == 0 || len(query) != len(target) {
		return 0
	}
	targetNorm := vectorNorm(target)
	if targetNorm == 0 {
		return 0
	}
	var dot float64
	for i := range query {
		dot += float64(query[i]) * float64(target[i])
	}
	return float32(dot / (queryNorm * targetNorm))
}

// matchMetadataEquals reports whether all key/value pairs in want match
// the corresponding entries in have. A nil want matches everything.
// Comparison uses reflect.DeepEqual so map and slice values are compared
// structurally — the same semantics qdrant's must.match would yield for
// JSON values.
func matchMetadataEquals(have, want map[string]any) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		got, ok := have[k]
		if !ok {
			return false
		}
		if !reflect.DeepEqual(got, v) {
			return false
		}
	}
	return true
}
