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
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// EmbedderFunc adapts a function into an Embedder, in the style of
// http.HandlerFunc.
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
