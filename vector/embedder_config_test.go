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

// Package vector_test holds the root package's external tests. They are
// external on purpose: asserting provider conformance needs to import
// vector/provider/..., and those packages already sit below the root
// package in the import graph.
package vector_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/vage/vector"
	"github.com/vogo/vage/vector/provider/openais"
	"github.com/vogo/vage/vector/provider/voyages"
)

// recordingServer answers any /embeddings call with a single 2-d vector
// and records the request line the caller produced, so config
// pass-through can be checked on the wire rather than through unexported
// fields.
type recordedRequest struct {
	path  string
	auth  string
	model string
	input []string
}

func recordingServer(t *testing.T, rec *recordedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.Unmarshal(raw, &body)

		rec.path = r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.model = body.Model
		rec.input = body.Input

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewEmbedderFromConfig_Dispatch checks that each provider value
// reaches the right implementation and that the three shared fields all
// arrive on the wire.
func TestNewEmbedderFromConfig_Dispatch(t *testing.T) {
	tests := []struct {
		name     string
		provider vector.Provider
		model    string
		wantType any
	}{
		{"openai", vector.ProviderOpenAI, "text-embedding-3-large", (*openais.Embedder)(nil)},
		{"voyage", vector.ProviderVoyage, "voyage-3.5", (*voyages.Embedder)(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec recordedRequest
			srv := recordingServer(t, &rec)

			emb, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
				Provider: tt.provider,
				APIKey:   "cfg-key",
				BaseURL:  srv.URL,
				Model:    tt.model,
			})
			if err != nil {
				t.Fatalf("NewEmbedderFromConfig: %v", err)
			}

			switch tt.wantType.(type) {
			case *openais.Embedder:
				if _, ok := emb.(*openais.Embedder); !ok {
					t.Fatalf("got %T, want *openais.Embedder", emb)
				}
			case *voyages.Embedder:
				if _, ok := emb.(*voyages.Embedder); !ok {
					t.Fatalf("got %T, want *voyages.Embedder", emb)
				}
			}

			v, err := emb.Embed(context.Background(), "hello")
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if len(v) != 2 || v[0] != 0.5 {
				t.Errorf("vector = %v", v)
			}

			if rec.path != "/embeddings" {
				t.Errorf("path = %q, want /embeddings (BaseURL not honoured)", rec.path)
			}
			if rec.auth != "Bearer cfg-key" {
				t.Errorf("Authorization = %q (APIKey not honoured)", rec.auth)
			}
			if rec.model != tt.model {
				t.Errorf("model = %q, want %q (Model not honoured)", rec.model, tt.model)
			}
			if len(rec.input) != 1 || rec.input[0] != "hello" {
				t.Errorf("input = %v", rec.input)
			}
		})
	}
}

// TestNewEmbedderFromConfig_OpenAIKeepsDefaultModel pins the deliberate
// asymmetry: OpenAI's historical default survives an empty Model.
func TestNewEmbedderFromConfig_OpenAIKeepsDefaultModel(t *testing.T) {
	var rec recordedRequest
	srv := recordingServer(t, &rec)

	emb, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: vector.ProviderOpenAI,
		APIKey:   "k",
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("NewEmbedderFromConfig: %v", err)
	}
	if _, err := emb.Embed(context.Background(), "hi"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if rec.model != openais.DefaultModel {
		t.Errorf("model = %q, want %q", rec.model, openais.DefaultModel)
	}
}

// TestNewEmbedderFromConfig_VoyageRequiresModel is the other half of that
// asymmetry — and it must surface the vendor's own sentinel.
func TestNewEmbedderFromConfig_VoyageRequiresModel(t *testing.T) {
	_, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: vector.ProviderVoyage,
		APIKey:   "k",
	})
	if !errors.Is(err, voyages.ErrMissingModel) {
		t.Fatalf("expected voyages.ErrMissingModel, got %v", err)
	}
}

func TestNewEmbedderFromConfig_ProviderRequired(t *testing.T) {
	_, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{APIKey: "k"})
	if !errors.Is(err, vector.ErrProviderRequired) {
		t.Fatalf("expected ErrProviderRequired, got %v", err)
	}
}

func TestNewEmbedderFromConfig_UnknownProvider(t *testing.T) {
	_, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: "cohere",
		APIKey:   "k",
		Model:    "embed-v3",
	})
	if !errors.Is(err, vector.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}

// TestNewEmbedderFromConfig_PublicEndpointNeedsKey doubles as the proof
// that an empty BaseURL falls back to the vendor's public endpoint: the
// key check only fires when the base URL is the official one.
func TestNewEmbedderFromConfig_PublicEndpointNeedsKey(t *testing.T) {
	_, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{Provider: vector.ProviderOpenAI})
	if !errors.Is(err, openais.ErrMissingAPIKey) {
		t.Fatalf("openai: expected ErrMissingAPIKey, got %v", err)
	}

	_, err = vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: vector.ProviderVoyage,
		Model:    "voyage-3.5",
	})
	if !errors.Is(err, voyages.ErrMissingAPIKey) {
		t.Fatalf("voyage: expected ErrMissingAPIKey, got %v", err)
	}
}

// TestNewEmbedderFromConfig_CustomEndpointAllowsEmptyKey covers local
// OpenAI-compatible servers and httptest wiring.
func TestNewEmbedderFromConfig_CustomEndpointAllowsEmptyKey(t *testing.T) {
	if _, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: vector.ProviderOpenAI,
		BaseURL:  "http://localhost:9",
	}); err != nil {
		t.Errorf("openai: %v", err)
	}

	if _, err := vector.NewEmbedderFromConfig(vector.EmbedderConfig{
		Provider: vector.ProviderVoyage,
		BaseURL:  "http://localhost:9",
		Model:    "voyage-3.5",
	}); err != nil {
		t.Errorf("voyage: %v", err)
	}
}

// TestNewEmbedderFromConfig_MakesNoNetworkCall pins the promise that
// construction is pure: the config points at a live server that fails
// the test if it is ever contacted during New.
func TestNewEmbedderFromConfig_MakesNoNetworkCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("constructor must not issue a request")
	}))
	defer srv.Close()

	for _, cfg := range []vector.EmbedderConfig{
		{Provider: vector.ProviderOpenAI, APIKey: "k", BaseURL: srv.URL},
		{Provider: vector.ProviderVoyage, APIKey: "k", BaseURL: srv.URL, Model: "voyage-3.5"},
	} {
		if _, err := vector.NewEmbedderFromConfig(cfg); err != nil {
			t.Fatalf("%s: %v", cfg.Provider, err)
		}
	}
}

// TestProviderConformance is the root-side home for the interface
// assertions the provider packages cannot make themselves: importing the
// root package from vector/provider/... would cycle back through
// NewEmbedderFromConfig. Every provider must satisfy all four
// capabilities so caller-side type assertions behave uniformly.
func TestProviderConformance(t *testing.T) {
	openaiEmb, err := openais.New(openais.WithBaseURL("http://x"), openais.WithAPIKey("k"))
	if err != nil {
		t.Fatalf("openais.New: %v", err)
	}
	voyageEmb, err := voyages.New(voyages.WithBaseURL("http://x"), voyages.WithModel("voyage-3.5"))
	if err != nil {
		t.Fatalf("voyages.New: %v", err)
	}

	embedders := map[string]vector.Embedder{
		"openais": openaiEmb,
		"voyages": voyageEmb,
	}

	for name, emb := range embedders {
		if _, ok := emb.(vector.BatchEmbedder); !ok {
			t.Errorf("%s does not satisfy BatchEmbedder", name)
		}
		if ne, ok := emb.(vector.NamedEmbedder); !ok {
			t.Errorf("%s does not satisfy NamedEmbedder", name)
		} else if ne.ModelName() == "" {
			t.Errorf("%s reports an empty model name", name)
		}
		if _, ok := emb.(vector.LimitedEmbedder); !ok {
			t.Errorf("%s does not satisfy LimitedEmbedder", name)
		}
	}
}

// TestProvidersShareEmptyQuerySentinel proves the internal contract point
// did its job: both providers return the root package's exported
// sentinel, so callers keep matching on vector.ErrEmptyQuery.
func TestProvidersShareEmptyQuerySentinel(t *testing.T) {
	openaiEmb, _ := openais.New(openais.WithBaseURL("http://x"))
	voyageEmb, _ := voyages.New(voyages.WithBaseURL("http://x"), voyages.WithModel("voyage-3.5"))

	for name, emb := range map[string]vector.Embedder{"openais": openaiEmb, "voyages": voyageEmb} {
		if _, err := emb.Embed(context.Background(), ""); !errors.Is(err, vector.ErrEmptyQuery) {
			t.Errorf("%s: expected vector.ErrEmptyQuery, got %v", name, err)
		}
	}
}
