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
	"math"
	"testing"
)

func TestEmbedderFunc_Roundtrip(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	var f EmbedderFunc = func(_ context.Context, text string) ([]float32, error) {
		if text == "" {
			return nil, errors.New("empty")
		}
		return want, nil
	}

	got, err := f.Embed(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%v want %v", i, got[i], want[i])
		}
	}

	if _, err := f.Embed(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty text")
	}
}

func TestHashEmbedder_DefaultDim(t *testing.T) {
	h := NewHashEmbedder(0)
	v, err := h.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(v) != HashEmbedderDefaultDim {
		t.Fatalf("len = %d, want %d", len(v), HashEmbedderDefaultDim)
	}
}

func TestHashEmbedder_Stability(t *testing.T) {
	h := NewHashEmbedder(64)
	a, _ := h.Embed(context.Background(), "Claude is the LLM")
	b, _ := h.Embed(context.Background(), "Claude is the LLM")
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("vectors differ at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestHashEmbedder_RoughSemantic(t *testing.T) {
	// Two texts with shared tokens should be more cosine-similar than two
	// texts with no shared tokens. This is the weakest possible "semantic"
	// signal but it's the only one HashEmbedder offers.
	h := NewHashEmbedder(256)

	a, _ := h.Embed(context.Background(), "the quick brown fox jumps")
	b, _ := h.Embed(context.Background(), "the quick brown fox runs")
	c, _ := h.Embed(context.Background(), "completely different unrelated tokens here")

	simAB := dotProduct(a, b)
	simAC := dotProduct(a, c)

	if simAB <= simAC {
		t.Fatalf("expected cos(a,b)=%v > cos(a,c)=%v", simAB, simAC)
	}
}

func TestHashEmbedder_Normalized(t *testing.T) {
	h := NewHashEmbedder(128)
	v, _ := h.Embed(context.Background(), "hello world from vage")

	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if math.Abs(norm-1.0) > 1e-5 {
		t.Fatalf("expected L2 norm ≈ 1, got %v", norm)
	}
}

func TestHashEmbedder_EmptyText(t *testing.T) {
	h := NewHashEmbedder(32)
	v, err := h.Embed(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(v) != 32 {
		t.Fatalf("len = %d, want 32", len(v))
	}
	for i, x := range v {
		if x != 0 {
			t.Fatalf("expected zero vector, v[%d]=%v", i, x)
		}
	}
}

func TestHashEmbedder_PunctuationOnly(t *testing.T) {
	// Tokenizer drops everything that is not letter/digit; result should
	// be a zero vector, not a panic.
	h := NewHashEmbedder(16)
	v, _ := h.Embed(context.Background(), "!!! ??? ...")
	for i, x := range v {
		if x != 0 {
			t.Fatalf("expected zero vector for punctuation input, v[%d]=%v", i, x)
		}
	}
}

// dotProduct returns sum(a[i]*b[i]). Both inputs must be the same length;
// for L2-normalized vectors this equals cosine similarity.
func dotProduct(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
