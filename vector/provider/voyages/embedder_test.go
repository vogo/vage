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

package voyages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vogo/vage/vector/internal/embedcore"
)

const testModel = "voyage-3.5"

// fakeServer points an Embedder at a handler and returns it with the
// server's cleanup. The handler is where request-shape assertions live,
// so serialisation drift surfaces in unit tests rather than on the first
// real call.
func fakeServer(t *testing.T, handler http.HandlerFunc, extra ...Option) (*Embedder, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	opts := append([]Option{WithBaseURL(srv.URL), WithAPIKey("test-key"), WithModel(testModel)}, extra...)
	e, err := New(opts...)
	if err != nil {
		srv.Close()
		t.Fatalf("New: %v", err)
	}
	return e, srv.Close
}

// decodeRequest reads the handler-side request body into the wire struct.
func decodeRequest(t *testing.T, r *http.Request) embeddingsRequest {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	var body embeddingsRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return body
}

// writeOK writes a synthetic Voyage response with sequential indices.
func writeOK(w http.ResponseWriter, vectors [][]float32) {
	resp := embeddingsResponse{Object: "list", Model: testModel}
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

func mustEmbedder(t *testing.T, opts ...Option) *Embedder {
	t.Helper()
	e, err := New(append([]Option{WithBaseURL("http://x"), WithModel(testModel)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestDefaultBaseURL_TargetsOfficialEndpoint(t *testing.T) {
	// The provider composes baseURL + "/embeddings"; pin the resulting
	// public URL so a stray edit to either half is caught.
	if got := DefaultBaseURL + "/embeddings"; got != "https://api.voyageai.com/v1/embeddings" {
		t.Fatalf("official endpoint = %q", got)
	}
}

func TestNew_RequiresAPIKeyForPublicEndpoint(t *testing.T) {
	if _, err := New(WithModel(testModel)); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestNew_AllowsEmptyAPIKeyForCustomBase(t *testing.T) {
	e, err := New(WithBaseURL("http://localhost:9"), WithModel(testModel))
	if err != nil {
		t.Fatalf("expected no error for custom base + empty key, got %v", err)
	}
	if e.apiKey != "" {
		t.Fatalf("expected empty api key, got %q", e.apiKey)
	}
}

func TestNew_RequiresModel(t *testing.T) {
	// Voyage has no framework-side default model: an omitted model is a
	// configuration error, not something to guess.
	if _, err := New(WithBaseURL("http://x")); !errors.Is(err, ErrMissingModel) {
		t.Fatalf("expected ErrMissingModel, got %v", err)
	}
	if _, err := New(WithAPIKey("k")); !errors.Is(err, ErrMissingModel) {
		t.Fatalf("expected ErrMissingModel with key but no model, got %v", err)
	}
}

func TestNew_RejectsUnknownInputType(t *testing.T) {
	_, err := New(WithBaseURL("http://x"), WithModel(testModel), WithInputType("passage"))
	if !errors.Is(err, ErrInvalidInputType) {
		t.Fatalf("expected ErrInvalidInputType, got %v", err)
	}
	if !strings.Contains(err.Error(), "passage") {
		t.Fatalf("error should name the offending value: %v", err)
	}
}

func TestNew_AcceptsQueryAndDocumentInputTypes(t *testing.T) {
	for _, it := range []InputType{"", InputTypeQuery, InputTypeDocument} {
		if _, err := New(WithBaseURL("http://x"), WithModel(testModel), WithInputType(it)); err != nil {
			t.Errorf("input type %q rejected: %v", it, err)
		}
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("Path = %q, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		body := decodeRequest(t, r)
		if body.Model != testModel {
			t.Errorf("Model = %q, want %q", body.Model, testModel)
		}
		if len(body.Input) != 1 || body.Input[0] != "hello" {
			t.Errorf("Input = %v, want single-element array", body.Input)
		}
		if body.InputType != "" {
			t.Errorf("InputType sent unexpectedly: %q", body.InputType)
		}
		if body.Truncation != nil {
			t.Errorf("Truncation sent unexpectedly: %v", *body.Truncation)
		}
		if body.OutputDimension != 0 {
			t.Errorf("OutputDimension sent unexpectedly: %d", body.OutputDimension)
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

func TestBatchEmbed_HappyPath(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequest(t, r)
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

func TestVendorOptions_SentOnTheWire(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequest(t, r)
		if body.InputType != string(InputTypeDocument) {
			t.Errorf("InputType = %q, want %q", body.InputType, InputTypeDocument)
		}
		if body.Truncation == nil || *body.Truncation {
			t.Errorf("Truncation = %v, want explicit false", body.Truncation)
		}
		if body.OutputDimension != 512 {
			t.Errorf("OutputDimension = %d, want 512", body.OutputDimension)
		}
		writeOK(w, [][]float32{make([]float32, 512)})
	}, WithInputType(InputTypeDocument), WithTruncation(false), WithOutputDimension(512))
	defer cleanup()

	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}

func TestInputType_IsInstanceLevelNotMethodInferred(t *testing.T) {
	// The same query-typed instance must send input_type=query from both
	// Embed and BatchEmbed: the framework never guesses intent from the
	// method name, so document-side callers must build their own instance.
	var seen []string
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequest(t, r)
		seen = append(seen, body.InputType)
		vs := make([][]float32, len(body.Input))
		for i := range vs {
			vs[i] = []float32{float32(i)}
		}
		writeOK(w, vs)
	}, WithInputType(InputTypeQuery))
	defer cleanup()

	if _, err := e.Embed(context.Background(), "q"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := e.BatchEmbed(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(seen) != 2 || seen[0] != "query" || seen[1] != "query" {
		t.Fatalf("input_type per call = %v, want [query query]", seen)
	}
}

func TestEmbed_EmptyText(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server should not have been called for empty text")
		writeOK(w, [][]float32{{}})
	})
	defer cleanup()

	if _, err := e.Embed(context.Background(), ""); !errors.Is(err, embedcore.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
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
	if !errors.Is(err, embedcore.ErrEmptyQuery) {
		t.Fatalf("expected ErrEmptyQuery, got %v", err)
	}
	if !strings.Contains(err.Error(), "texts[1]") {
		t.Fatalf("error should name the offending index: %v", err)
	}
}

func TestEmbed_ErrorResponse(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Provided API key is invalid."}`))
	})
	defer cleanup()

	_, err := e.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "API key is invalid") {
		t.Fatalf("error did not surface status + body: %v", err)
	}
	if strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("error leaked credentials: %v", err)
	}
}

func TestEmbed_TransportError(t *testing.T) {
	// Port 1 is reserved and never listening: exercises the transport
	// failure path without depending on network reachability.
	e, err := New(WithBaseURL("http://127.0.0.1:1"), WithModel(testModel))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := e.Embed(context.Background(), "hi"); err == nil {
		t.Fatal("expected transport error")
	} else if !strings.Contains(err.Error(), "do request") {
		t.Fatalf("error did not preserve the transport cause: %v", err)
	}
}

func TestEmbed_ContextCancelled(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeOK(w, [][]float32{{1}})
	})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Embed(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in the chain, got %v", err)
	}
}

func TestEmbed_MalformedJSON(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": [`))
	})
	defer cleanup()

	if _, err := e.Embed(context.Background(), "hi"); err == nil {
		t.Fatal("expected decode error")
	} else if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error did not name the decode step: %v", err)
	}
}

func TestBatchEmbed_OutOfOrderResponse(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Indices reversed. The embedder must restore input order so the
		// caller never has to know the wire contract.
		_ = json.NewEncoder(w).Encode(embeddingsResponse{
			Object: "list",
			Model:  testModel,
			Data: []embeddingsResponseItem{
				{Index: 2, Embedding: []float32{2, 2}},
				{Index: 1, Embedding: []float32{1, 1}},
				{Index: 0, Embedding: []float32{0, 0}},
			},
		})
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

// TestBatchEmbed_InconsistentIndices covers every way the response can
// fail to map cleanly onto the input. Each must fail the whole call: a
// vector paired with the wrong text is silent corruption.
func TestBatchEmbed_InconsistentIndices(t *testing.T) {
	tests := []struct {
		name string
		data []embeddingsResponseItem
		want string
	}{
		{
			name: "index out of range",
			data: []embeddingsResponseItem{
				{Index: 0, Embedding: []float32{0}},
				{Index: 7, Embedding: []float32{1}},
			},
			want: "out of range",
		},
		{
			name: "negative index",
			data: []embeddingsResponseItem{
				{Index: -1, Embedding: []float32{0}},
				{Index: 1, Embedding: []float32{1}},
			},
			want: "out of range",
		},
		{
			name: "duplicate index",
			data: []embeddingsResponseItem{
				{Index: 0, Embedding: []float32{0}},
				{Index: 0, Embedding: []float32{1}},
			},
			want: "duplicate response index",
		},
		{
			name: "count mismatch",
			data: []embeddingsResponseItem{
				{Index: 0, Embedding: []float32{0}},
			},
			want: "expected 2 vectors, got 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(embeddingsResponse{
					Object: "list", Model: testModel, Data: tt.data,
				})
			})
			defer cleanup()

			_, err := e.BatchEmbed(context.Background(), []string{"a", "b"})
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestBatchEmbed_NullEmbedding covers a response with the right item
// count and in-range indices but a null vector: index 1 is never filled,
// so the call fails rather than handing back a nil vector the store would
// later reject with a confusing dimension error.
func TestBatchEmbed_NullEmbedding(t *testing.T) {
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"object":"embedding","index":0,"embedding":[0]},
			{"object":"embedding","index":1,"embedding":null}
		]}`))
	})
	defer cleanup()

	_, err := e.BatchEmbed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for missing vector")
	}
	if !strings.Contains(err.Error(), "missing vector at index 1") {
		t.Fatalf("error = %v, want it to name the unfilled index", err)
	}
}

func TestEmbed_SingleVectorCountGuard(t *testing.T) {
	// A response that decodes but carries the wrong cardinality for a
	// single-text call must not silently return the first vector.
	e, cleanup := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeOK(w, [][]float32{{1}, {2}})
	})
	defer cleanup()

	if _, err := e.Embed(context.Background(), "hi"); err == nil {
		t.Fatal("expected cardinality error")
	}
}

func TestModelName(t *testing.T) {
	if got := mustEmbedder(t).ModelName(); got != testModel {
		t.Fatalf("ModelName = %q, want %q", got, testModel)
	}
	e := mustEmbedder(t, WithModel("voyage-code-3"))
	if got := e.ModelName(); got != "voyage-code-3" {
		t.Fatalf("ModelName = %q", got)
	}
}

func TestMaxInputTokens_UnknownByDefault(t *testing.T) {
	// Voyage context lengths differ per model and move between
	// generations; unconfigured means "unknown" (0), never a guess.
	if got := mustEmbedder(t).MaxInputTokens(); got != 0 {
		t.Fatalf("MaxInputTokens = %d, want 0 (unknown)", got)
	}
	if got := mustEmbedder(t, WithMaxInputTokens(32000)).MaxInputTokens(); got != 32000 {
		t.Fatalf("MaxInputTokens = %d, want 32000", got)
	}
}

func TestWithHTTPClient_NilKeepsDefault(t *testing.T) {
	e := mustEmbedder(t, WithHTTPClient(nil))
	if e.httpClient == nil {
		t.Fatal("nil client must fall back to the internal default")
	}
}

func TestWithBaseURL_TrimsTrailingSlash(t *testing.T) {
	e := mustEmbedder(t, WithBaseURL("http://example.com/v1/"))
	if e.baseURL != "http://example.com/v1" {
		t.Fatalf("baseURL = %q", e.baseURL)
	}
}
