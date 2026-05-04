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

// Package openai implements vector.Embedder against the OpenAI
// /v1/embeddings endpoint.
//
// The implementation is a thin standalone HTTP client (no dependency on
// aimodel, which currently scopes to chat completions only). It targets
// the text-embedding-3 family and is forward-compatible with any
// successor that follows the same JSON contract.
//
// Capabilities:
//
//   - vector.Embedder         — single-text embed
//   - vector.BatchEmbedder    — multi-text embed in one round-trip
//   - vector.NamedEmbedder    — reports the configured model
//   - vector.LimitedEmbedder  — reports the model's max input tokens
//
// Errors are deliberately wrapped (not bare) so the caller can route on
// HTTP status (rate limit / auth / 5xx) without parsing strings.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vogo/vage/vector"
)

// DefaultBaseURL is the OpenAI public API endpoint. Override via
// WithBaseURL for OpenAI-compatible providers (Azure, vLLM, etc.) or for
// httptest in unit tests.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultModel is the default embedding model. text-embedding-3-small
// is the "small + cheap + supports `dimensions` parameter" workhorse.
const DefaultModel = "text-embedding-3-small"

// MaxInputTokensTextEmbedding3 is the published limit for OpenAI's
// text-embedding-3 family. Reported via LimitedEmbedder so ingestion
// paths can pre-truncate.
const MaxInputTokensTextEmbedding3 = 8191

// defaultHTTPTimeout balances "real network latency to OpenAI" against
// "do not let a stuck connection hang an agent run forever". 30s is
// comfortably above OpenAI's typical p99 (~3s) without being so loose
// that it masks a stuck endpoint.
const defaultHTTPTimeout = 30 * time.Second

// ErrMissingAPIKey is returned when the embedder is constructed without
// an API key and the configured base URL still points at the public
// OpenAI endpoint. Local httptest servers are a legitimate empty-key
// case — see WithBaseURL.
var ErrMissingAPIKey = errors.New("openai embedder: missing API key")

// Embedder embeds text via the OpenAI /v1/embeddings endpoint.
//
// Concurrency: safe for concurrent use. The underlying *http.Client is
// shared across calls, which is the standard library's contract.
type Embedder struct {
	apiKey     string
	baseURL    string
	model      string
	dimensions int          // 0 -> server default for the chosen model
	httpClient *http.Client // never nil after New
	maxTokens  int
}

// Option is a functional option for New.
type Option func(*Embedder)

// WithAPIKey sets the bearer token. Required when BaseURL targets the
// public OpenAI API.
func WithAPIKey(k string) Option { return func(e *Embedder) { e.apiKey = k } }

// WithBaseURL overrides the API endpoint. Must NOT include a trailing
// slash. Use this for OpenAI-compatible providers and for httptest
// servers in unit tests.
func WithBaseURL(u string) Option {
	return func(e *Embedder) { e.baseURL = strings.TrimRight(u, "/") }
}

// WithModel selects the embedding model. Empty falls back to
// DefaultModel.
func WithModel(m string) Option { return func(e *Embedder) { e.model = m } }

// WithDimensions requests server-side dimensionality reduction.
// Supported by the text-embedding-3 family only; older models reject
// the parameter. 0 (the default) sends no `dimensions` field, so the
// server returns the model's native vector length.
//
// This is the primary lever for the dimension-alignment validation
// promised in §4.9: paired with WithLockedDimension on the store, it
// lets callers fix a smaller dimension up-front rather than relying on
// first-Add lock.
func WithDimensions(d int) Option { return func(e *Embedder) { e.dimensions = d } }

// WithHTTPClient injects a custom *http.Client (e.g., one with retry
// middleware, custom TLS, or shorter timeouts). nil falls back to the
// internal default.
func WithHTTPClient(c *http.Client) Option {
	return func(e *Embedder) {
		if c != nil {
			e.httpClient = c
		}
	}
}

// WithMaxInputTokens overrides the advisory token limit reported via
// LimitedEmbedder.MaxInputTokens. 0 falls back to
// MaxInputTokensTextEmbedding3.
func WithMaxInputTokens(n int) Option {
	return func(e *Embedder) {
		if n > 0 {
			e.maxTokens = n
		}
	}
}

// New constructs an Embedder. An empty API key is allowed only when the
// base URL has been overridden (typical for httptest servers); against
// the public DefaultBaseURL it returns ErrMissingAPIKey eagerly so the
// failure surfaces at setup time, not on the first Embed call.
func New(opts ...Option) (*Embedder, error) {
	e := &Embedder{
		baseURL:    DefaultBaseURL,
		model:      DefaultModel,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
		maxTokens:  MaxInputTokensTextEmbedding3,
	}
	for _, o := range opts {
		o(e)
	}
	if e.apiKey == "" && e.baseURL == DefaultBaseURL {
		return nil, ErrMissingAPIKey
	}
	if e.model == "" {
		e.model = DefaultModel
	}
	return e, nil
}

// Embed embeds a single text.
//
// An empty input returns ErrEmptyQuery from the vector package so the
// caller can route on the standard sentinel. Errors from the HTTP
// transport, non-2xx responses, and malformed bodies are wrapped with
// context.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, vector.ErrEmptyQuery
	}
	vs, err := e.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("openai embedder: expected 1 vector, got %d", len(vs))
	}
	return vs[0], nil
}

// BatchEmbed embeds multiple texts in one round-trip.
//
// All inputs must be non-empty; an empty string anywhere in the slice
// returns ErrEmptyQuery so the caller fixes the input rather than
// silently producing a zero vector. Empty input slice returns nil with
// no error (parity with HashEmbedder.BatchEmbed).
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, t := range texts {
		if t == "" {
			return nil, fmt.Errorf("openai embedder: texts[%d]: %w", i, vector.ErrEmptyQuery)
		}
	}
	return e.embed(ctx, texts)
}

// ModelName implements vector.NamedEmbedder.
func (e *Embedder) ModelName() string { return e.model }

// MaxInputTokens implements vector.LimitedEmbedder.
func (e *Embedder) MaxInputTokens() int { return e.maxTokens }

// embed is the shared transport path for Embed and BatchEmbed.
func (e *Embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := embeddingsRequest{
		Model: e.model,
		Input: texts,
	}
	if e.dimensions > 0 {
		body.Dimensions = e.dimensions
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai embedder: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai embedder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embedder: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embedder: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the OpenAI error body verbatim — it is small, JSON, and
		// callers want to see the rate-limit / auth / model-name reason.
		return nil, fmt.Errorf("openai embedder: http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("openai embedder: decode response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("openai embedder: expected %d vectors, got %d", len(texts), len(parsed.Data))
	}

	// OpenAI documents that response order matches input order, but the
	// JSON also carries an explicit Index. Sort defensively when out of
	// order so a future spec change does not silently misalign vectors
	// with their source texts.
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai embedder: response index %d out of range [0,%d)", d.Index, len(texts))
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("openai embedder: duplicate response index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("openai embedder: missing vector at index %d", i)
		}
	}
	return out, nil
}

// embeddingsRequest is the JSON request body for /v1/embeddings.
//
// Input is `[]string`. The endpoint also accepts a single string and an
// array of token IDs; we always send a string array so the response
// shape is uniformly `data[].embedding` regardless of batch size.
type embeddingsRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

// embeddingsResponse mirrors the documented shape of the OpenAI
// /v1/embeddings response. Usage is intentionally omitted — vage does
// not yet wire token accounting through embedder calls; add when the
// observability story (P0-5) lands.
type embeddingsResponse struct {
	Object string                   `json:"object"`
	Model  string                   `json:"model"`
	Data   []embeddingsResponseItem `json:"data"`
}

type embeddingsResponseItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// Compile-time conformance: cover every capability the package
// promises in its doc.
var (
	_ vector.Embedder        = (*Embedder)(nil)
	_ vector.BatchEmbedder   = (*Embedder)(nil)
	_ vector.NamedEmbedder   = (*Embedder)(nil)
	_ vector.LimitedEmbedder = (*Embedder)(nil)
)
