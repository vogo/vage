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
	// BraveName is the canonical provider id used in configs and envelopes.
	BraveName            = "brave"
	braveDefaultEndpoint = "https://api.search.brave.com/res/v1/web/search"
	braveMaxBodyBytes    = 1 << 20
	// braveMaxCount is Brave's documented per-request upper bound.
	braveMaxCount = 20
)

// BraveProvider implements Provider against api.search.brave.com.
type BraveProvider struct {
	apiKey     string
	httpClient *http.Client
	endpoint   string
	userAgent  string
}

// BraveOption configures a BraveProvider.
type BraveOption func(*BraveProvider)

// WithBraveHTTPClient overrides the HTTP client (defaults to http.DefaultClient).
func WithBraveHTTPClient(c *http.Client) BraveOption {
	return func(p *BraveProvider) {
		if c != nil {
			p.httpClient = c
		}
	}
}

// WithBraveEndpoint overrides the endpoint URL. Used by tests.
func WithBraveEndpoint(url string) BraveOption {
	return func(p *BraveProvider) {
		if strings.TrimSpace(url) != "" {
			p.endpoint = url
		}
	}
}

// WithBraveUserAgent overrides the User-Agent header.
func WithBraveUserAgent(ua string) BraveOption {
	return func(p *BraveProvider) {
		if strings.TrimSpace(ua) != "" {
			p.userAgent = ua
		}
	}
}

// NewBrave builds a Brave provider. Returns nil when apiKey is empty.
func NewBrave(apiKey string, opts ...BraveOption) *BraveProvider {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	p := &BraveProvider{
		apiKey:     apiKey,
		httpClient: http.DefaultClient,
		endpoint:   braveDefaultEndpoint,
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider id.
func (p *BraveProvider) Name() string { return BraveName }

type braveResponse struct {
	Web struct {
		Results []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
			PageAge     string `json:"page_age"`
		} `json:"results"`
	} `json:"web"`
}

// Search executes a Brave GET and translates the response into Result list.
func (p *BraveProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults > braveMaxCount {
		maxResults = braveMaxCount
	}

	q := url.Values{}
	q.Set("q", query)
	if maxResults > 0 {
		q.Set("count", strconv.Itoa(maxResults))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidAPIKey
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, braveMaxBodyBytes))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(bodyBytes)}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, braveMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("brave: read body: %w", err)
	}

	var parsed braveResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, asParseError(fmt.Errorf("brave: decode: %w", err))
	}

	out := make([]Result, 0, len(parsed.Web.Results))
	for _, r := range parsed.Web.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, Result{
			URL:         strings.TrimSpace(r.URL),
			Title:       strings.TrimSpace(r.Title),
			Snippet:     truncateSnippet(r.Description),
			PublishedAt: strings.TrimSpace(r.PageAge),
		})
	}
	return out, nil
}
