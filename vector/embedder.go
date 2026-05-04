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
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Embedder maps a textual query/document to a fixed-length vector.
//
// The interface is deliberately single-method so real backends (OpenAI,
// Anthropic, voyage) can implement it with a single API call. Embedding
// dimension is validated by the VectorStore (first-Add lock or explicit
// constructor option) and is not exposed on Embedder — embedders may
// legitimately produce different lengths per call (e.g. OpenAI
// text-embedding-3 with the `dimensions` parameter).
//
// Capabilities beyond single-shot embedding are surfaced via optional
// sibling interfaces (BatchEmbedder, NamedEmbedder, LimitedEmbedder).
// Callers detect them with a type assertion; embedders implement only
// what they actually support. This keeps the core surface minimal and
// avoids forcing every backend to fake batching or token-limit
// introspection it does not have.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// BatchEmbedder is an optional capability for backends that can embed
// many texts in one round-trip. Real APIs (OpenAI /embeddings, voyage,
// cohere) accept arrays natively and amortize HTTP / TLS / auth across
// the batch — for ingestion paths this is a 5-50x throughput win over
// looping Embed.
//
// Returned slice MUST be the same length as `texts`, with results in the
// input order. An empty input returns an empty slice (no error). The
// dimension of every returned vector MUST match — backends that produce
// variable-length vectors (e.g. OpenAI with explicit `dimensions`) MUST
// use the same dimension across the batch.
//
// Callers detect support with a type assertion:
//
//	if be, ok := emb.(BatchEmbedder); ok { ... } else { /* fall back to Embed loop */ }
type BatchEmbedder interface {
	BatchEmbed(ctx context.Context, texts []string) ([][]float32, error)
}

// NamedEmbedder lets a backend report the underlying model identifier
// (e.g. "text-embedding-3-small", "voyage-3-large", "hash-embedder").
// Used for log lines, metrics labels, and write-time dimension drift
// detection.
//
// The string is opaque to the framework — it is not parsed or validated.
// Backends SHOULD return a stable identifier across an embedder
// instance's lifetime so a hash of (model_name, dim) can fingerprint a
// store collection.
type NamedEmbedder interface {
	ModelName() string
}

// LimitedEmbedder reports the maximum input size the backend will
// accept, in tokens. The bound is advisory: callers MAY pre-truncate
// long inputs to fit, or split into batches.
//
// 0 (or implementations that do not satisfy LimitedEmbedder at all)
// means "unknown / no advertised limit"; callers SHOULD then fall back
// to a safe default (e.g. 8192 tokens for text-embedding-3 family).
type LimitedEmbedder interface {
	MaxInputTokens() int
}

// EmbedderFunc adapts a function into an Embedder, in the style of
// http.HandlerFunc. It does NOT implement the sibling capabilities;
// callers needing batch / named / limited semantics should implement
// the interfaces explicitly on a struct.
type EmbedderFunc func(ctx context.Context, text string) ([]float32, error)

// Embed implements Embedder.
func (f EmbedderFunc) Embed(ctx context.Context, text string) ([]float32, error) {
	return f(ctx, text)
}

// HashEmbedder is a deterministic, dependency-free Embedder intended for
// tests and local experimentation only. It tokenizes the input on
// non-letter/non-digit boundaries, hashes each lowercased token into a
// fixed-size bag, and L2-normalizes the result so cosine similarity
// reduces to a dot product.
//
// NOT for production: it has no semantic understanding beyond
// bag-of-tokens overlap. Use a real embedding API (OpenAI, voyage,
// Anthropic) for any quality-sensitive workload.
type HashEmbedder struct {
	dim int
}

// HashEmbedderDefaultDim is the default embedding dimension used when
// NewHashEmbedder is called with a non-positive dim.
const HashEmbedderDefaultDim = 128

// NewHashEmbedder returns a HashEmbedder of the requested dimension. A
// non-positive dim falls back to HashEmbedderDefaultDim.
func NewHashEmbedder(dim int) *HashEmbedder {
	if dim <= 0 {
		dim = HashEmbedderDefaultDim
	}
	return &HashEmbedder{dim: dim}
}

// HashEmbedderModelName is the stable identifier reported by
// HashEmbedder.ModelName. Tests that pin behaviour on a specific model
// label can compare against this constant rather than a string literal.
const HashEmbedderModelName = "hash-embedder"

// HashEmbedderMaxInputTokens is the (very generous) advisory limit
// reported by HashEmbedder. The hash path has no real ceiling, but
// returning a finite value keeps callers that gate on
// LimitedEmbedder.MaxInputTokens working uniformly.
const HashEmbedderMaxInputTokens = 1 << 20

// Embed implements Embedder. It is deterministic for a given (text, dim)
// pair; an empty input returns a zero vector of the configured length.
func (h *HashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	out := make([]float32, h.dim)
	if text == "" {
		return out, nil
	}

	for _, tok := range tokenizeForHash(text) {
		hsh := fnv.New32a()
		_, _ = hsh.Write([]byte(tok))
		bucket := int(hsh.Sum32()) % h.dim
		if bucket < 0 {
			bucket += h.dim
		}
		out[bucket]++
	}

	// L2 normalize so cosine == dot product.
	var norm float64
	for _, v := range out {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return out, nil
	}
	inv := float32(1.0 / math.Sqrt(norm))
	for i := range out {
		out[i] *= inv
	}
	return out, nil
}

// BatchEmbed implements BatchEmbedder by looping Embed. HashEmbedder is
// in-process and CPU-cheap, so the trivial implementation is fine; the
// method exists primarily so the type-assertion code path on the caller
// side is exercised by tests without spinning up a real backend.
func (h *HashEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := h.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// ModelName implements NamedEmbedder. It always returns
// HashEmbedderModelName so dimension/model-fingerprinting code paths
// have a stable identifier even in test-only setups.
func (*HashEmbedder) ModelName() string { return HashEmbedderModelName }

// MaxInputTokens implements LimitedEmbedder with a generous advisory
// limit. HashEmbedder itself has no real ceiling, but returning a
// finite value keeps callers that gate on the limit (e.g. ingestion
// truncation) uniform across embedders.
func (*HashEmbedder) MaxInputTokens() int { return HashEmbedderMaxInputTokens }

// Compile-time conformance: HashEmbedder implements Embedder plus all
// three optional sibling interfaces. Real backends (OpenAI etc.) only
// need to satisfy the subset they actually support.
var (
	_ Embedder        = (*HashEmbedder)(nil)
	_ BatchEmbedder   = (*HashEmbedder)(nil)
	_ NamedEmbedder   = (*HashEmbedder)(nil)
	_ LimitedEmbedder = (*HashEmbedder)(nil)
)

// tokenizeForHash splits text into lowercased tokens on
// non-letter/non-digit boundaries. Empty strings are dropped. The
// behaviour is intentionally simple — HashEmbedder is a fixture, not a
// linguistic tool.
func tokenizeForHash(text string) []string {
	if text == "" {
		return nil
	}
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) == 0 {
		return nil
	}
	for i, t := range tokens {
		tokens[i] = strings.ToLower(t)
	}
	return tokens
}
