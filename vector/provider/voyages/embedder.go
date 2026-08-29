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

// Package voyages implements vector.Embedder against the Voyage AI
// /v1/embeddings endpoint.
//
// Voyage is the embedding service Anthropic's own documentation points
// Claude applications at; Anthropic ships no native embedding API, so
// there is deliberately no vector/provider/anthropics counterpart. The
// package sits beside vector/provider/openais as a peer: the two never
// import each other, and neither goes through largemodel or aimodel —
// embedding stays layered apart from chat.
//
// Capabilities:
//
//   - vector.Embedder         — single-text embed
//   - vector.BatchEmbedder    — multi-text embed in one round-trip
//   - vector.NamedEmbedder    — reports the configured model
//   - vector.LimitedEmbedder  — reports a configured input-token limit
//
// This package does not import the vector root package: the root
// config-driven constructor imports every provider, so the dependency
// only runs downward. Interface conformance is asserted by the root
// package's external tests.
//
// Vendor-specific knobs (input_type, truncation, output_dimension) are
// exposed here as Options only. The provider-neutral
// vector.EmbedderConfig deliberately carries none of them.
package voyages

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

	"github.com/vogo/vage/vector/internal/embedcore"
)

// DefaultBaseURL is the Voyage public API endpoint. Override via
// WithBaseURL for a gateway/proxy or for httptest in unit tests.
const DefaultBaseURL = "https://api.voyageai.com/v1"

// defaultHTTPTimeout mirrors the OpenAI provider: comfortably above the
// service's typical latency without letting a stuck connection hang an
// agent run forever.
const defaultHTTPTimeout = 30 * time.Second

// InputType is Voyage's retrieval-optimization hint. Voyage prepends a
// different internal prompt for queries and for documents, which
// measurably improves retrieval quality when both sides are embedded
// with the matching type.
//
// The framework never infers it from the method being called: Embed and
// BatchEmbed are used for both ingestion and recall. Callers that want
// the optimization create two instances of the same model and output
// dimension — one InputTypeDocument for writes, one InputTypeQuery for
// searches — and inject the right one at each site.
type InputType string

// Input type values accepted by Voyage. The zero value (unset) sends no
// input_type at all, which is the API's own default.
const (
	InputTypeQuery    InputType = "query"
	InputTypeDocument InputType = "document"
)

// Sentinel errors returned by New. They surface at construction time,
// before any network call, so a misconfigured embedder fails at wiring
// rather than on the first recall.
var (
	// ErrMissingAPIKey is returned when no API key is configured and the
	// base URL still points at the public Voyage endpoint. A custom base
	// URL (local gateway, httptest) is a legitimate empty-key case.
	ErrMissingAPIKey = errors.New("voyage embedder: missing API key")

	// ErrMissingModel is returned when no model is configured. Unlike
	// OpenAI, Voyage requires the field, and its recommended model moves
	// with each generation — pinning a default in the framework would
	// silently change what callers embed with. The choice stays explicit.
	ErrMissingModel = errors.New("voyage embedder: missing model")

	// ErrInvalidInputType is returned when WithInputType is given
	// anything other than InputTypeQuery or InputTypeDocument.
	ErrInvalidInputType = errors.New("voyage embedder: invalid input type")
)

// Embedder embeds text via the Voyage /v1/embeddings endpoint.
//
// Concurrency: safe for concurrent use. The underlying *http.Client is
// shared across calls, which is the standard library's contract.
type Embedder struct {
	apiKey     string
	baseURL    string
	model      string
	inputType  InputType
	truncation *bool // nil -> omit the field, letting the server default apply
	outputDim  int   // 0 -> model's native dimension
	httpClient *http.Client
	maxTokens  int
}

// Option is a functional option for New.
type Option func(*Embedder)

// WithAPIKey sets the bearer token. Required when BaseURL targets the
// public Voyage API.
func WithAPIKey(k string) Option { return func(e *Embedder) { e.apiKey = k } }

// WithBaseURL overrides the API endpoint. A trailing slash is trimmed.
// Use it for gateways and for httptest servers in unit tests.
func WithBaseURL(u string) Option {
	return func(e *Embedder) { e.baseURL = strings.TrimRight(u, "/") }
}

// WithModel selects the embedding model (e.g. "voyage-3.5",
// "voyage-3-large", "voyage-code-3"). Required — see ErrMissingModel.
func WithModel(m string) Option { return func(e *Embedder) { e.model = m } }

// WithInputType sets the retrieval-optimization hint for every call this
// instance makes. See InputType for why it is instance-level rather than
// per-call. An unrecognized value makes New fail with
// ErrInvalidInputType.
func WithInputType(t InputType) Option { return func(e *Embedder) { e.inputType = t } }

// WithTruncation controls whether Voyage silently truncates inputs that
// exceed the model's context length (true) or rejects them with an error
// (false). Unset omits the field and takes the server's own default.
func WithTruncation(b bool) Option { return func(e *Embedder) { e.truncation = &b } }

// WithOutputDimension requests server-side dimensionality reduction,
// supported by the Matryoshka-trained models. 0 (the default) sends no
// field, so the server returns the model's native vector length.
//
// This is the Voyage counterpart of the OpenAI `dimensions` parameter:
// paired with the store's locked dimension it lets callers fix a smaller
// vector width up-front instead of relying on first-Add lock.
func WithOutputDimension(d int) Option {
	return func(e *Embedder) {
		if d > 0 {
			e.outputDim = d
		}
	}
}

// WithHTTPClient injects a custom *http.Client (retry middleware, custom
// TLS, tighter timeouts). nil falls back to the internal default.
func WithHTTPClient(c *http.Client) Option {
	return func(e *Embedder) {
		if c != nil {
			e.httpClient = c
		}
	}
}

// WithMaxInputTokens sets the advisory limit reported via
// LimitedEmbedder.MaxInputTokens.
//
// There is no default: Voyage's per-model context lengths differ and are
// revised between model generations, so an unconfigured embedder reports
// 0 ("unknown") rather than a guess that ingestion code might truncate
// against.
func WithMaxInputTokens(n int) Option {
	return func(e *Embedder) {
		if n > 0 {
			e.maxTokens = n
		}
	}
}

// New constructs an Embedder. It performs no network I/O: every
// configuration problem it can detect (missing key on the public
// endpoint, missing model, bad input type) is reported here.
func New(opts ...Option) (*Embedder, error) {
	e := &Embedder{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
	for _, o := range opts {
		o(e)
	}
	if e.apiKey == "" && e.baseURL == DefaultBaseURL {
		return nil, ErrMissingAPIKey
	}
	if e.model == "" {
		return nil, ErrMissingModel
	}
	switch e.inputType {
	case "", InputTypeQuery, InputTypeDocument:
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidInputType, e.inputType)
	}
	return e, nil
}

// Embed embeds a single text.
//
// Empty input returns vector.ErrEmptyQuery without issuing a request, so
// the caller fixes the input instead of paying for a meaningless vector.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, embedcore.ErrEmptyQuery
	}
	vs, err := e.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("voyage embedder: expected 1 vector, got %d", len(vs))
	}
	return vs[0], nil
}

// BatchEmbed embeds multiple texts in one round-trip.
//
// An empty slice returns nil with no error and no request (parity with
// the OpenAI provider and HashEmbedder). An empty string anywhere in the
// slice returns vector.ErrEmptyQuery, again without a request.
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, t := range texts {
		if t == "" {
			return nil, fmt.Errorf("voyage embedder: texts[%d]: %w", i, embedcore.ErrEmptyQuery)
		}
	}
	return e.embed(ctx, texts)
}

// ModelName implements vector.NamedEmbedder.
func (e *Embedder) ModelName() string { return e.model }

// MaxInputTokens implements vector.LimitedEmbedder. It returns 0
// ("unknown") unless WithMaxInputTokens was supplied — see that option.
func (e *Embedder) MaxInputTokens() int { return e.maxTokens }

// embed is the shared transport path for Embed and BatchEmbed. It does
// not retry: recall callers own their own degradation policy, and a
// hidden retry would multiply cost and latency behind their backs.
func (e *Embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := embeddingsRequest{
		Model:      e.model,
		Input:      texts,
		InputType:  string(e.inputType),
		Truncation: e.truncation,
	}
	if e.outputDim > 0 {
		body.OutputDimension = e.outputDim
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("voyage embedder: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("voyage embedder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage embedder: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voyage embedder: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Status plus the server's own error body — enough to tell auth
		// from rate limit from a bad model name. Request headers (and
		// therefore the API key) are never echoed.
		return nil, fmt.Errorf("voyage embedder: http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("voyage embedder: decode response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("voyage embedder: expected %d vectors, got %d", len(texts), len(parsed.Data))
	}

	// Voyage documents that data[].index identifies the source position.
	// We rebuild the slice through that index rather than trusting array
	// order: pairing a vector with the wrong text is silent corruption
	// that only shows up later as bad recall, so any inconsistency fails
	// the whole call instead.
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("voyage embedder: response index %d out of range [0,%d)", d.Index, len(texts))
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("voyage embedder: duplicate response index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("voyage embedder: missing vector at index %d", i)
		}
	}
	return out, nil
}

// embeddingsRequest is the JSON request body for Voyage /v1/embeddings.
//
// Input is always a string array even for a single text, so the response
// shape is uniformly data[].embedding regardless of batch size.
//
// output_dtype and encoding_format are deliberately absent: vector.Embedder
// is defined over []float32, and quantized or base64 responses would need a
// lossy conversion the framework should not perform implicitly.
type embeddingsRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	InputType       string   `json:"input_type,omitempty"`
	Truncation      *bool    `json:"truncation,omitempty"`
	OutputDimension int      `json:"output_dimension,omitempty"`
}

// embeddingsResponse mirrors the documented Voyage response shape. Usage
// is omitted for the same reason as in the OpenAI provider: vage does not
// yet route embedding token accounting through hooks.
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
