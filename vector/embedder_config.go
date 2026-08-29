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
	"errors"
	"fmt"

	"github.com/vogo/vage/vector/provider/openais"
	"github.com/vogo/vage/vector/provider/voyages"
)

// Provider names a backing embedding service. It is the discriminator of
// EmbedderConfig — the one field a caller must decide before anything
// else in the config means something.
type Provider string

// Supported embedding providers.
//
// There is deliberately no ProviderAnthropic: Anthropic ships no native
// embedding API and its own documentation points Claude applications at
// Voyage, so ProviderVoyage is the Claude-ecosystem answer.
const (
	// ProviderOpenAI targets the OpenAI /v1/embeddings endpoint and the
	// text-embedding-3 family. See vector/provider/openais.
	ProviderOpenAI Provider = "openai"

	// ProviderVoyage targets the Voyage AI /v1/embeddings endpoint. See
	// vector/provider/voyages.
	ProviderVoyage Provider = "voyage"
)

// Errors returned by NewEmbedderFromConfig for a config it cannot act on.
// Both surface before any network call.
var (
	// ErrProviderRequired is returned when Config.Provider is empty. The
	// choice is never guessed: silently defaulting to one vendor would
	// send a caller's text — and their bill — somewhere they did not ask
	// for.
	ErrProviderRequired = errors.New("vector: embedder provider is required")

	// ErrUnknownProvider is returned for a provider value this build does
	// not implement.
	ErrUnknownProvider = errors.New("vector: unknown embedder provider")
)

// EmbedderConfig is the provider-neutral description of an embedding
// backend — the shape that survives a YAML file or an env-driven wiring
// layer.
//
// It carries only what both providers genuinely share. Vendor-specific
// capabilities (OpenAI's `dimensions`, Voyage's `input_type`,
// `truncation`, `output_dimension`) are NOT here: folding them into one
// struct would mean either a lossy union or fields that are silently
// ignored half the time. Callers needing them construct the provider
// package directly:
//
//	emb, err := voyages.New(
//	    voyages.WithAPIKey(key),
//	    voyages.WithModel("voyage-3.5"),
//	    voyages.WithInputType(voyages.InputTypeDocument),
//	)
//
// Both paths return the same vector.Embedder, so downstream recall code
// is unaffected by which one was used.
type EmbedderConfig struct {
	// Provider selects the backend. Required.
	Provider Provider

	// APIKey is the bearer credential. This package never reads it from
	// the environment — the caller owns credential sourcing. It may be
	// empty only when BaseURL points somewhere other than the vendor's
	// public endpoint (local compatible service, httptest).
	APIKey string

	// BaseURL overrides the API endpoint. Empty uses the provider's
	// official base URL.
	BaseURL string

	// Model selects the embedding model. Empty keeps OpenAI's existing
	// default; Voyage requires it explicitly (see NewEmbedderFromConfig).
	Model string
}

// NewEmbedderFromConfig constructs the Embedder described by cfg.
//
// It performs no network I/O: what it returns is either a usable
// embedder or a configuration error, so misconfiguration surfaces at
// wiring time rather than on the first recall.
//
// Provider-specific validation is left to the provider constructors, so
// the errors a caller gets back are the vendor's own sentinels — e.g.
// errors.Is(err, voyages.ErrMissingModel). One asymmetry is deliberate:
// an empty Model keeps OpenAI's historical default, while Voyage rejects
// it, because Voyage's API requires the field and its recommended model
// changes between generations — pinning one in the framework would
// silently move what callers embed with.
func NewEmbedderFromConfig(cfg EmbedderConfig) (Embedder, error) {
	switch cfg.Provider {
	case "":
		return nil, ErrProviderRequired

	case ProviderOpenAI:
		opts := []openais.Option{openais.WithAPIKey(cfg.APIKey)}
		if cfg.BaseURL != "" {
			opts = append(opts, openais.WithBaseURL(cfg.BaseURL))
		}
		if cfg.Model != "" {
			opts = append(opts, openais.WithModel(cfg.Model))
		}
		return openais.New(opts...)

	case ProviderVoyage:
		opts := []voyages.Option{
			voyages.WithAPIKey(cfg.APIKey),
			voyages.WithModel(cfg.Model),
		}
		if cfg.BaseURL != "" {
			opts = append(opts, voyages.WithBaseURL(cfg.BaseURL))
		}
		return voyages.New(opts...)

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, cfg.Provider)
	}
}
