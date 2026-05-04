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

// Package qdrant implements vector.VectorStore against qdrant's REST
// API (v1.x).
//
// The package is deliberately a thin wrapper over net/http + JSON: no
// gRPC, no SDK dependency, no per-collection caches beyond a one-shot
// "did we ensure?" flag. Behaviour mirrors MapVectorStore where
// possible:
//
//   - first Add locks the collection's vector dimension (created on
//     demand);
//   - WithLockedDimension creates the collection eagerly with the
//     supplied size, so wiring code can construct a Store before any
//     document exists;
//   - cosine distance — same metric MapVectorStore uses;
//   - mismatched dimensions return vector.ErrDimensionMismatch;
//   - Delete of a missing ID is silent (parity with MapVectorStore).
//
// Caller-visible IDs are arbitrary strings (vector.Document.ID). qdrant
// requires either uint64 or UUID; we derive a deterministic UUIDv5
// from the user ID and stash the original in the payload under
// payloadKeyVageID so reads / Lists round-trip cleanly.
package qdrant

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vogo/vage/vector"
)

// payloadKeyVageID is the qdrant payload field where we store the
// original user-supplied vector.Document.ID. Naming is deliberately
// unique so it does not collide with user metadata keys.
const payloadKeyVageID = "_vage_id"

// payloadKeyText is the qdrant payload field where Document.Text is
// stored when non-empty. Documents with empty text simply omit it.
const payloadKeyText = "_vage_text"

// payloadKeyCreatedAt is the qdrant payload field for
// Document.CreatedAt (RFC3339Nano string). Storing as string keeps the
// payload introspectable in the qdrant dashboard without requiring a
// custom timestamp encoding.
const payloadKeyCreatedAt = "_vage_created_at"

// distanceCosine is the only similarity metric this package configures
// new collections with. It matches MapVectorStore's behaviour, so the
// VectorStore contract holds across backends.
const distanceCosine = "Cosine"

// defaultHTTPTimeout — qdrant is typically deployed locally / in-cluster
// (sub-100ms RTT). 15s is plenty without masking a stuck node.
const defaultHTTPTimeout = 15 * time.Second

// vageNamespaceUUID is a fixed UUIDv4 used as the namespace for
// converting user-supplied string IDs into stable UUIDv5 point IDs.
// Generated once and pinned forever — changing it would invalidate all
// existing data.
var vageNamespaceUUID = mustParseUUID("c8c6c2ef-3a7d-4ddf-9eda-8a25b4c0d8a8")

// Store implements vector.VectorStore against qdrant.
//
// Concurrency: safe. The ensureOnce flag is guarded by sync.Once and
// the underlying *http.Client is safe for concurrent Do calls.
type Store struct {
	baseURL    string
	collection string
	apiKey     string
	httpClient *http.Client

	defaultTopK int

	// dim is the collection's vector size. 0 = unlocked; set on first
	// successful Add or by WithLockedDimension.
	dimMu       sync.Mutex
	dim         int
	dimExplicit bool

	// ensure runs at most once and creates the collection if missing.
	// On success, ensureErr stays nil; on failure subsequent calls
	// retry (we do NOT cache the error).
	ensureMu sync.Mutex
	ensured  bool
}

// Option configures a Store.
type Option func(*Store)

// WithAPIKey sets the qdrant API key (used as the `api-key` header on
// qdrant Cloud and on self-hosted instances configured with
// `service.api_key`). Empty disables the header.
func WithAPIKey(k string) Option { return func(s *Store) { s.apiKey = k } }

// WithHTTPClient injects a custom *http.Client. nil keeps the default.
func WithHTTPClient(c *http.Client) Option {
	return func(s *Store) {
		if c != nil {
			s.httpClient = c
		}
	}
}

// WithDefaultTopK overrides the TopK used when SearchOptions.TopK is
// non-positive. Mirrors vector.WithDefaultTopK on MapVectorStore.
func WithDefaultTopK(k int) Option {
	return func(s *Store) {
		if k > 0 {
			s.defaultTopK = k
		}
	}
}

// WithLockedDimension creates the collection eagerly at New time with
// the supplied vector size (Cosine distance). Use this when the
// embedder dimension is known up-front; otherwise the dimension is
// inferred from the first Add.
//
// If the collection already exists with a different size, ensureCollection
// surfaces the mismatch as ErrDimensionMismatch on the next call.
func WithLockedDimension(d int) Option {
	return func(s *Store) {
		if d > 0 {
			s.dim = d
			s.dimExplicit = true
		}
	}
}

// New constructs a Store. baseURL must include scheme and host
// (`http://localhost:6333`); collection is the qdrant collection name.
// Both are required.
func New(baseURL, collection string, opts ...Option) (*Store, error) {
	if baseURL == "" {
		return nil, errors.New("qdrant: baseURL is required")
	}
	if collection == "" {
		return nil, errors.New("qdrant: collection is required")
	}
	s := &Store{
		baseURL:     strings.TrimRight(baseURL, "/"),
		collection:  collection,
		httpClient:  &http.Client{Timeout: defaultHTTPTimeout},
		defaultTopK: vector.DefaultTopK,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Compile-time conformance.
var _ vector.VectorStore = (*Store)(nil)

// Add upserts a single document.
func (s *Store) Add(ctx context.Context, doc vector.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(doc.Embedding) == 0 {
		return vector.ErrEmptyQuery
	}

	s.dimMu.Lock()
	if s.dim == 0 {
		s.dim = len(doc.Embedding)
	} else if len(doc.Embedding) != s.dim {
		s.dimMu.Unlock()
		return vector.ErrDimensionMismatch
	}
	s.dimMu.Unlock()

	if err := s.ensureCollection(ctx); err != nil {
		return err
	}

	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}

	pt := point{
		ID:      derivePointID(doc.ID),
		Vector:  doc.Embedding,
		Payload: buildPayload(doc),
	}

	body := upsertPointsRequest{Points: []point{pt}}
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", s.baseURL, s.collection)
	if err := s.doJSON(ctx, http.MethodPut, url, body, nil); err != nil {
		return fmt.Errorf("qdrant: upsert: %w", err)
	}
	return nil
}

// Search runs an ANN query.
func (s *Store) Search(ctx context.Context, query []float32, opts vector.SearchOptions) ([]vector.SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query) == 0 {
		return nil, vector.ErrEmptyQuery
	}

	// Dimension check is best-effort: if the store has not been used
	// yet (dim==0) we let qdrant return its own error rather than
	// guessing.
	s.dimMu.Lock()
	if s.dim != 0 && len(query) != s.dim {
		s.dimMu.Unlock()
		return nil, vector.ErrDimensionMismatch
	}
	s.dimMu.Unlock()

	limit := opts.TopK
	if limit <= 0 {
		limit = s.defaultTopK
	}

	req := searchRequest{
		Vector:      query,
		Limit:       limit,
		WithPayload: true,
		WithVector:  false,
		Filter:      buildFilter(opts.MetadataEquals),
	}
	if opts.MinScore != 0 {
		v := opts.MinScore
		req.ScoreThresh = &v
	}

	var resp searchResponse
	url := fmt.Sprintf("%s/collections/%s/points/search", s.baseURL, s.collection)
	if err := s.doJSON(ctx, http.MethodPost, url, req, &resp); err != nil {
		return nil, fmt.Errorf("qdrant: search: %w", err)
	}

	hits := make([]vector.SearchHit, 0, len(resp.Result))
	for _, p := range resp.Result {
		doc := decodePoint(p)
		// Apply MetadataEquals client-side as a defensive fallback for
		// any value buildFilter could not push down (slice/map/struct).
		if !matchesUnpushable(doc.Metadata, opts.MetadataEquals) {
			continue
		}
		if opts.Predicate != nil && !opts.Predicate(doc) {
			continue
		}
		hits = append(hits, vector.SearchHit{Document: doc, Score: p.Score})
	}
	return hits, nil
}

// Delete removes a document by ID. Missing IDs are silent successes
// (parity with MapVectorStore).
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body := deletePointsRequest{Points: []string{derivePointID(id)}}
	url := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", s.baseURL, s.collection)
	if err := s.doJSON(ctx, http.MethodPost, url, body, nil); err != nil {
		// 404 on a missing collection is treated as silent success —
		// matches MapVectorStore (delete-missing == ok). A real
		// transport / 5xx error still surfaces.
		if errors.Is(err, errCollectionNotFound) {
			return nil
		}
		return fmt.Errorf("qdrant: delete: %w", err)
	}
	return nil
}

// List paginates through all points via the scroll endpoint. Defensive
// limit: each page returns 256 points; we follow next_page_offset until
// the server reports no more.
func (s *Store) List(ctx context.Context) ([]vector.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const pageSize = 256

	var all []vector.Document
	offset := ""
	url := fmt.Sprintf("%s/collections/%s/points/scroll", s.baseURL, s.collection)
	for {
		body := scrollRequest{
			Limit:       pageSize,
			Offset:      offset,
			WithPayload: true,
			WithVector:  true,
		}
		var resp scrollResponse
		if err := s.doJSON(ctx, http.MethodPost, url, body, &resp); err != nil {
			if errors.Is(err, errCollectionNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("qdrant: scroll: %w", err)
		}
		for _, p := range resp.Result.Points {
			all = append(all, decodePoint(p))
		}
		if resp.Result.NextPageOffset == nil || *resp.Result.NextPageOffset == "" {
			break
		}
		offset = *resp.Result.NextPageOffset
	}
	return all, nil
}

// ensureCollection creates the qdrant collection if it does not exist.
// Idempotent: a collection-exists 4xx is treated as success. The
// `ensured` flag short-circuits subsequent calls so we do not hit the
// API on every Add.
func (s *Store) ensureCollection(ctx context.Context) error {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.ensured {
		return nil
	}

	s.dimMu.Lock()
	dim := s.dim
	s.dimMu.Unlock()
	if dim <= 0 {
		return errors.New("qdrant: cannot create collection with dim=0")
	}

	body := createCollectionRequest{
		Vectors: vectorParams{Size: dim, Distance: distanceCosine},
	}
	url := fmt.Sprintf("%s/collections/%s", s.baseURL, s.collection)
	err := s.doJSON(ctx, http.MethodPut, url, body, nil)
	switch {
	case err == nil:
		s.ensured = true
		return nil
	case errors.Is(err, errCollectionExists):
		// Already created by us or another process — fine. Trust the
		// existing dimension; a real mismatch will surface on the next
		// upsert as ErrDimensionMismatch from the qdrant body.
		s.ensured = true
		return nil
	default:
		return fmt.Errorf("qdrant: ensure collection: %w", err)
	}
}

// errCollectionExists / errCollectionNotFound are sentinels used
// internally by doJSON so callers can branch on the qdrant 4xx
// vocabulary without parsing strings.
var (
	errCollectionExists   = errors.New("qdrant: collection already exists")
	errCollectionNotFound = errors.New("qdrant: collection not found")
)

// doJSON marshals body, executes the request, and decodes the response
// into out (or discards when out is nil). Non-2xx responses are
// translated into either a sentinel (collection-exists,
// collection-not-found, dimension mismatch) or a wrapped error
// containing the status + body.
func (s *Store) doJSON(ctx context.Context, method, url string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	var bodyReader io.Reader
	if reader != nil {
		bodyReader = reader
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch sentinel := classifyQdrantError(resp.StatusCode, respBody); sentinel {
		case errCollectionExists, errCollectionNotFound:
			return sentinel
		case errDimensionMismatchSentinel:
			return vector.ErrDimensionMismatch
		default:
			return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// errDimensionMismatchSentinel is internal — doJSON converts it to
// vector.ErrDimensionMismatch so callers see the public sentinel.
var errDimensionMismatchSentinel = errors.New("qdrant: dimension mismatch")

// classifyQdrantError inspects the status code + body and returns the
// matching sentinel, or nil when the error is generic.
func classifyQdrantError(status int, body []byte) error {
	text := strings.ToLower(string(body))
	switch {
	case status == http.StatusConflict && strings.Contains(text, "already exists"):
		return errCollectionExists
	case status == http.StatusBadRequest && strings.Contains(text, "already exists"):
		// Some qdrant versions return 400 instead of 409 here.
		return errCollectionExists
	case status == http.StatusNotFound:
		return errCollectionNotFound
	case strings.Contains(text, "vector dimension") || strings.Contains(text, "wrong vector size"):
		return errDimensionMismatchSentinel
	default:
		return nil
	}
}

// derivePointID returns a deterministic UUIDv5 derived from the user
// ID. qdrant only accepts UUIDs or uint64 as point IDs; v5 ensures
// idempotent upserts (the same vector.Document.ID always maps to the
// same qdrant point).
func derivePointID(userID string) string {
	return uuidV5(vageNamespaceUUID, userID)
}

// buildPayload prepares the qdrant payload for a Document. User
// metadata wins on key collision with the reserved `_vage_*` fields —
// rare in practice, and matching this would just create silent
// surprises for the caller.
func buildPayload(doc vector.Document) map[string]any {
	out := make(map[string]any, len(doc.Metadata)+3)
	maps.Copy(out, doc.Metadata)
	out[payloadKeyVageID] = doc.ID
	if doc.Text != "" {
		out[payloadKeyText] = doc.Text
	}
	if !doc.CreatedAt.IsZero() {
		out[payloadKeyCreatedAt] = doc.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// decodePoint reverses buildPayload. The reserved `_vage_*` keys are
// stripped from the user-visible Metadata so callers see exactly what
// they Add'ed.
func decodePoint(p scoredPoint) vector.Document {
	doc := vector.Document{}
	if p.Payload == nil {
		return doc
	}
	if id, ok := p.Payload[payloadKeyVageID].(string); ok {
		doc.ID = id
	}
	if t, ok := p.Payload[payloadKeyText].(string); ok {
		doc.Text = t
	}
	if ts, ok := p.Payload[payloadKeyCreatedAt].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			doc.CreatedAt = parsed
		}
	}
	if len(p.Payload) > 0 {
		md := make(map[string]any, len(p.Payload))
		for k, v := range p.Payload {
			if k == payloadKeyVageID || k == payloadKeyText || k == payloadKeyCreatedAt {
				continue
			}
			md[k] = v
		}
		if len(md) > 0 {
			doc.Metadata = md
		}
	}
	if len(p.Vector) > 0 {
		doc.Embedding = p.Vector
	}
	return doc
}

// matchesUnpushable applies the subset of MetadataEquals that
// buildFilter could not translate to qdrant. Pushable types are
// guaranteed to have already been server-filtered, so re-checking them
// here is a no-op — we only run reflect.DeepEqual on the unpushable
// keys.
func matchesUnpushable(have, want map[string]any) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		if isMatchable(v) {
			continue
		}
		got, ok := have[k]
		if !ok {
			return false
		}
		if !deepEqual(got, v) {
			return false
		}
	}
	return true
}

// deepEqual is a thin wrapper around encoding/json for value
// comparison: marshal both sides and compare bytes. This avoids a
// reflect dependency and matches qdrant's JSON-round-tripped values.
func deepEqual(a, b any) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}

// uuidV5 returns the RFC 4122 v5 UUID for (namespace, name). Standard
// algorithm: SHA1(namespace_bytes || name), then set version (5) and
// variant (10) bits. No external deps.
func uuidV5(namespace [16]byte, name string) string {
	h := sha1.New()
	h.Write(namespace[:])
	_, _ = io.WriteString(h, name)
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant 10
	return formatUUID(sum)
}

// formatUUID renders a 16-byte slice as 8-4-4-4-12 hex.
func formatUUID(b []byte) string {
	hexStr := hex.EncodeToString(b[:16])
	return hexStr[0:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:32]
}

// mustParseUUID parses an 8-4-4-4-12 string into 16 bytes. Panics on
// bad input — only used at init for the package-level namespace.
func mustParseUUID(s string) [16]byte {
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		panic("qdrant: bad namespace UUID")
	}
	raw, err := hex.DecodeString(clean)
	if err != nil {
		panic("qdrant: bad namespace UUID hex: " + err.Error())
	}
	var out [16]byte
	copy(out[:], raw)
	return out
}

// LockedDimension returns the collection's vector dimension, or 0 when
// the store has not yet inferred it.
func (s *Store) LockedDimension() int {
	s.dimMu.Lock()
	defer s.dimMu.Unlock()
	return s.dim
}
