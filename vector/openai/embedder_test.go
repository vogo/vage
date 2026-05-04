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

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vogo/vage/vector"
)

// fakeServer accepts a handler and returns an Embedder pointed at it,
// plus a cleanup that the caller defers. The fake is intentionally
// strict: it asserts the request body shape so accidental drift in the
// embedder serialisation surfaces in unit tests rather than at first
// real call.
func fakeServer(t *testing.T, handler http.HandlerFunc) (*Embedder, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	e, err := New(WithBaseURL(srv.URL), WithAPIKey("test-key"))
	if err != nil {
		srv.Close()
		t.Fatalf("New: %v", err)
	}
	return e, srv.Close
}

func TestNew_RequiresAPIKeyForPublicEndpoint(t *testing.T) {
	if _, err := New(); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestNew_AllowsEmptyAPIKeyForCustomBase(t *testing.T) {
	e, err := New(WithBaseURL("http://localhost:9"))
	if err != nil {
		t.Fatalf("expected no error for custom base + empty key, got %v", err)
	}
	if e.apiKey != "" {
		t.Fatalf("expected empty api key, got %q", e.apiKey)
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		var body embeddingsRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != DefaultModel {
			t.Errorf("Model = %q, want %q", body.Model, DefaultModel)
		}
		if len(body.Input) != 1 || body.Input[0] != "hello" {
			t.Errorf("Input = %v", body.Input)
		}
		if body.Dimensions != 0 {
			t.Errorf("Dimensions sent unexpectedly: %d", body.Dimensions)
		}

		writeOK(w, [][]float32{{0.1, 0.2, 0.3}})
	})
	defer cleanup()

	v, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != 3 || v[0] != 0.1 {
		t.Fatalf("vector = %v", v)
	}
}

func TestEmbed_EmptyText(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server should not have been called for empty text")
		writeOK(w, [][]float32{{}})
	})
	defer cleanup()

	if _, err := e.Embed(context.Background(), ""); !errors.Is(err, vector.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestEmbed_ErrorResponse(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	})
	defer cleanup()

	_, err := e.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("error did not surface status + body: %v", err)
	}
}

func TestBatchEmbed_HappyPath(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body embeddingsRequest
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if len(body.Input) != 3 {
			t.Errorf("Input len = %d, want 3", len(body.Input))
		}
		writeOK(w, [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}})
	})
	defer cleanup()

	vs, err := e.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("len(vs) = %d", len(vs))
	}
	if vs[1][1] != 1 {
		t.Fatalf("vs[1] = %v", vs[1])
	}
}

func TestBatchEmbed_EmptyInput(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server should not have been called for empty input")
		writeOK(w, nil)
	})
	defer cleanup()

	vs, err := e.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if vs != nil {
		t.Fatalf("expected nil, got %v", vs)
	}
}

func TestBatchEmbed_RejectsEmptyElement(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server should not have been called when input has empty element")
		writeOK(w, nil)
	})
	defer cleanup()

	_, err := e.BatchEmbed(context.Background(), []string{"ok", "", "ok"})
	if !errors.Is(err, vector.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestBatchEmbed_OutOfOrderResponse(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Server returns indices reversed. Embedder must reorder so the
		// caller does not need to know the API contract.
		resp := embeddingsResponse{
			Object: "list",
			Model:  DefaultModel,
			Data: []embeddingsResponseItem{
				{Index: 2, Embedding: []float32{2, 2}},
				{Index: 1, Embedding: []float32{1, 1}},
				{Index: 0, Embedding: []float32{0, 0}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer cleanup()

	vs, err := e.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if vs[0][0] != 0 || vs[1][0] != 1 || vs[2][0] != 2 {
		t.Fatalf("reorder failed: %v", vs)
	}
}

func TestEmbed_DimensionsOption(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body embeddingsRequest
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body.Dimensions != 256 {
			t.Errorf("Dimensions = %d, want 256", body.Dimensions)
		}
		writeOK(w, [][]float32{make([]float32, 256)})
	})
	defer cleanup()

	e2, _ := New(WithBaseURL(strings.TrimSuffix(e.baseURL, "")), WithAPIKey("k"), WithDimensions(256))
	if _, err := e2.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}

func TestSiblingInterfaceConformance(t *testing.T) {
	// An OpenAI Embedder must satisfy all four capabilities so the
	// type-assertion path matches HashEmbedder behaviourally.
	var e vector.Embedder = mustEmbedder(t)

	if _, ok := e.(vector.BatchEmbedder); !ok {
		t.Error("openai.Embedder does not satisfy BatchEmbedder")
	}
	if ne, ok := e.(vector.NamedEmbedder); !ok {
		t.Error("openai.Embedder does not satisfy NamedEmbedder")
	} else if ne.ModelName() != DefaultModel {
		t.Errorf("ModelName = %q, want %q", ne.ModelName(), DefaultModel)
	}
	if le, ok := e.(vector.LimitedEmbedder); !ok {
		t.Error("openai.Embedder does not satisfy LimitedEmbedder")
	} else if le.MaxInputTokens() != MaxInputTokensTextEmbedding3 {
		t.Errorf("MaxInputTokens = %d, want %d", le.MaxInputTokens(), MaxInputTokensTextEmbedding3)
	}
}

func TestModelName_Override(t *testing.T) {
	e, err := New(WithBaseURL("http://x"), WithModel("text-embedding-3-large"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.ModelName() != "text-embedding-3-large" {
		t.Fatalf("ModelName = %q", e.ModelName())
	}
}

func TestMaxInputTokens_Override(t *testing.T) {
	e, err := New(WithBaseURL("http://x"), WithMaxInputTokens(4096))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.MaxInputTokens() != 4096 {
		t.Fatalf("MaxInputTokens = %d", e.MaxInputTokens())
	}
}

func mustEmbedder(t *testing.T) *Embedder {
	t.Helper()
	e, err := New(WithBaseURL("http://x"), WithAPIKey("k"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// writeOK writes a synthetic embeddings response with sequential indices.
// Mirrors the OpenAI documented shape.
func writeOK(w http.ResponseWriter, vectors [][]float32) {
	resp := embeddingsResponse{Object: "list", Model: DefaultModel}
	for i, v := range vectors {
		resp.Data = append(resp.Data, embeddingsResponseItem{
			Object:    "embedding",
			Index:     i,
			Embedding: v,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
