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

package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// SearXNGName is the canonical provider id used in configs and envelopes.
	SearXNGName         = "searxng"
	searxngMaxBodyBytes = 1 << 20
	// searxngSearchPath is the suffix appended to a base URL that does not
	// already end with /search. Self-hosted instances publish the search
	// endpoint at /search regardless of the root mount point.
	searxngSearchPath = "/search"
)

// searxngAllowedCategories is the SearXNG built-in category whitelist. We
// only forward categories the upstream is guaranteed to recognise so a
// poorly-chosen LLM topic cannot cause a 400 or be silently ignored.
var searxngAllowedCategories = map[string]struct{}{
	"general": {}, "news": {}, "images": {}, "videos": {},
	"music": {}, "files": {}, "it": {}, "science": {},
	"social media": {}, "map": {},
}

// SearXNGProvider implements Provider against a self-hosted SearXNG instance.
//
// Unlike Tavily/Brave, SearXNG does not require an API key — the operator
// runs their own instance — but recent SearXNG releases ship a `limiter`
// plugin that rejects non-browser User-Agents and rate-limits the JSON
// format. Callers who hit a 429 should:
//   - enable `search.formats: [html, json]` in the instance settings.yml,
//   - set `server.limiter: false` (or whitelist the agent UA), and
//   - pass a browser-style WithSearXNGUserAgent so requests are not flagged.
type SearXNGProvider struct {
	endpoint   string
	httpClient *http.Client
	userAgent  string
	apiKey     string // optional; sent as Authorization: Bearer when non-empty
	language   string
	categories string
}

// SearXNGOption configures a SearXNGProvider.
type SearXNGOption func(*SearXNGProvider)

// WithSearXNGHTTPClient overrides the HTTP client (defaults to http.DefaultClient).
func WithSearXNGHTTPClient(c *http.Client) SearXNGOption {
	return func(p *SearXNGProvider) {
		if c != nil {
			p.httpClient = c
		}
	}
}

// WithSearXNGUserAgent overrides the User-Agent header. SearXNG's limiter
// plugin rejects identifiable bot UAs; operators should pass a browser-style
// UA when calling against an instance with the limiter enabled.
func WithSearXNGUserAgent(ua string) SearXNGOption {
	return func(p *SearXNGProvider) {
		if strings.TrimSpace(ua) != "" {
			p.userAgent = ua
		}
	}
}

// WithSearXNGAPIKey sets an optional bearer token forwarded as
// `Authorization: Bearer <key>`. Most public SearXNG instances do not use
// one; this exists for operators who put the instance behind a gateway.
func WithSearXNGAPIKey(key string) SearXNGOption {
	return func(p *SearXNGProvider) {
		if strings.TrimSpace(key) != "" {
			p.apiKey = strings.TrimSpace(key)
		}
	}
}

// WithSearXNGLanguage sets the default language hint (e.g. "en", "zh-CN").
// Forwarded as the `language` query parameter; SearXNG's "auto" is used
// when empty.
func WithSearXNGLanguage(lang string) SearXNGOption {
	return func(p *SearXNGProvider) {
		if strings.TrimSpace(lang) != "" {
			p.language = strings.TrimSpace(lang)
		}
	}
}

// WithSearXNGCategories sets a default category list (comma-separated).
// Each token must be on the SearXNG built-in whitelist
// (general/news/images/...); unknown tokens are dropped at request time.
// Per-call topic via TopicFromContext takes precedence when set.
func WithSearXNGCategories(cats string) SearXNGOption {
	return func(p *SearXNGProvider) {
		if strings.TrimSpace(cats) != "" {
			p.categories = strings.TrimSpace(cats)
		}
	}
}

// NewSearXNG builds a SearXNG provider. Returns nil when endpoint is empty
// so the caller can detect "not configured" without a separate Validate call
// — SearXNG has no canonical public endpoint and the operator must supply
// one explicitly.
func NewSearXNG(endpoint string, opts ...SearXNGOption) *SearXNGProvider {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	p := &SearXNGProvider{
		endpoint:   normalizeSearXNGEndpoint(endpoint),
		httpClient: http.DefaultClient,
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider id.
func (p *SearXNGProvider) Name() string { return SearXNGName }

type searxngResponse struct {
	Results []struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		Content       string `json:"content"`
		PublishedDate string `json:"publishedDate"`
	} `json:"results"`
	// SearXNG returns a top-level message field on plugin / engine errors
	// (HTTP still 200). We surface it through HTTPError so the Tool layer
	// reports provider_error rather than pretending the call succeeded.
	Message string `json:"message,omitempty"`
}

// Search executes a SearXNG GET and translates the response into Result list.
func (p *SearXNGProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("safesearch", "1")
	q.Set("pageno", "1")
	if p.language != "" {
		q.Set("language", p.language)
	}
	if cats := p.resolveCategories(ctx); cats != "" {
		q.Set("categories", cats)
	}
	// SearXNG does not document a server-side result-count cap; we still
	// hint via `results_count` for instances that honour it. Tool-level
	// hardMaxResults still trims the final response to the LLM.
	if maxResults > 0 {
		q.Set("results_count", strconv.Itoa(maxResults))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("searxng: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidAPIKey
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, searxngMaxBodyBytes))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(bodyBytes)}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, searxngMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("searxng: read body: %w", err)
	}

	var parsed searxngResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, asParseError(fmt.Errorf("searxng: decode: %w", err))
	}

	if parsed.Message != "" && len(parsed.Results) == 0 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: parsed.Message}
	}

	out := make([]Result, 0, len(parsed.Results))
	for i, r := range parsed.Results {
		if maxResults > 0 && i >= maxResults {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, Result{
			URL:         strings.TrimSpace(r.URL),
			Title:       strings.TrimSpace(r.Title),
			Snippet:     truncateSnippet(r.Content),
			PublishedAt: strings.TrimSpace(r.PublishedDate),
		})
	}
	return out, nil
}

// resolveCategories returns the comma-joined category list to forward, with
// per-call topic (from context) winning over the configured default. Tokens
// not on searxngAllowedCategories are dropped to avoid 400 responses.
func (p *SearXNGProvider) resolveCategories(ctx context.Context) string {
	raw := strings.TrimSpace(TopicFromContext(ctx))
	if raw == "" {
		raw = p.categories
	}
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, t := range parts {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := searxngAllowedCategories[t]; ok {
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}

// normalizeSearXNGEndpoint accepts either a base URL ("http://host") or a
// fully-qualified search URL ("http://host/search"). It returns the form
// that resolves to the JSON search endpoint when a query string is appended.
func normalizeSearXNGEndpoint(raw string) string {
	trimmed := strings.TrimRight(raw, "/")
	if strings.HasSuffix(trimmed, searxngSearchPath) {
		return trimmed
	}
	return trimmed + searxngSearchPath
}
