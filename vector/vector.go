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

// Package vector defines a small, pluggable vector-recall surface used by
// vage/context's VectorRecallSource and other future retrieval-driven
// sources. The interfaces are intentionally minimal so real backends
// (qdrant, pgvector, chroma, pinecone) can implement them without
// contortions while the in-process MapVectorStore covers tests and local
// experimentation.
package vector

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. Backends and callers compare with errors.Is; messages
// are stable and safe for log assertions in tests.
var (
	// ErrEmptyQuery indicates Search was called with a nil/empty vector.
	ErrEmptyQuery = errors.New("vector: empty query")

	// ErrDimensionMismatch is returned when an Add or Search operation
	// supplies a vector whose length differs from the store's locked
	// dimension. Backends should fail fast (Add time, not Search time).
	ErrDimensionMismatch = errors.New("vector: embedding dimension mismatch")

	// ErrNotFound is the canonical "missing document" error. MapVectorStore
	// follows memory.MapStore in treating Delete-of-missing as silent
	// success; callers wanting strict semantics should use a backend that
	// returns ErrNotFound (or wrap one).
	ErrNotFound = errors.New("vector: document not found")

	// ErrNotSupported lets backends opt out of optional methods (e.g. List
	// on a remote store that does not support enumeration).
	ErrNotSupported = errors.New("vector: operation not supported by backend")
)

// Document is a single indexed item.
//
// Embedding length must match the store's locked dimension. Stores SHOULD
// stamp CreatedAt with time.Now() on Add when zero; this is a convenience
// for in-process usage so tests need not pass timestamps explicitly.
type Document struct {
	ID        string         `json:"id"`
	Text      string         `json:"text"`
	Embedding []float32      `json:"embedding,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at,omitzero"`
}

// SearchHit pairs a Document with the similarity score to the query
// vector. Higher scores mean "more similar"; the exact range depends on
// the backend's metric (cosine for MapVectorStore is in [-1, 1]).
type SearchHit struct {
	Document Document
	Score    float32
}

// SearchOptions controls a Search call. The shape is intentionally minimal
// and forward-compatible:
//
//   - MetadataEquals is a declarative equality filter that real backends
//     can push down (qdrant must.match, pgvector @>).
//   - Predicate is a client-side escape hatch. Backends MAY apply it AFTER
//     the vector search is complete, so it does not reduce candidate count
//     for the ANN index — it only post-filters. On large stores Predicate
//     can be slow; prefer MetadataEquals when possible.
type SearchOptions struct {
	TopK           int                   // 0 -> store default
	MinScore       float32               // 0 -> no threshold
	MetadataEquals map[string]any        // optional declarative filter
	Predicate      func(d Document) bool // optional client-side post-filter
}

// VectorStore is the pluggable backend interface. Implementations MUST be
// safe for concurrent use unless documented otherwise.
//
// Concurrency: MapVectorStore uses sync.RWMutex. Real backends typically
// inherit safety from their underlying client.
//
// Errors: backends should return one of the sentinel errors above when
// applicable so callers can use errors.Is for routing.
type VectorStore interface {
	// Add inserts or replaces a document. Stores lock the embedding
	// dimension on the first Add (or at construction via an explicit
	// option) and reject mismatched future Adds with ErrDimensionMismatch.
	Add(ctx context.Context, doc Document) error

	// Search returns up to TopK hits whose embedding is closest to query
	// under the store's similarity metric. Results are ordered highest
	// score first. Empty query returns ErrEmptyQuery; mismatched length
	// returns ErrDimensionMismatch.
	Search(ctx context.Context, query []float32, opts SearchOptions) ([]SearchHit, error)

	// Delete removes the document with the given ID. Implementations MAY
	// return ErrNotFound when the ID does not exist; MapVectorStore
	// silently succeeds (parity with memory.MapStore).
	Delete(ctx context.Context, id string) error

	// List returns every stored document (without scores). Useful for
	// introspection and tests; production stores MAY return
	// ErrNotSupported.
	List(ctx context.Context) ([]Document, error)
}
