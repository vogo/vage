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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// FirecrawlName is the canonical provider id used in configs and envelopes.
	FirecrawlName            = "firecrawl"
	firecrawlDefaultEndpoint = "https://api.firecrawl.dev/v1/search"
	firecrawlMaxBodyBytes    = 1 << 20
)

// FirecrawlProvider implements Provider against api.firecrawl.dev.
//
// Firecrawl exposes a POST /v1/search endpoint that returns a JSON envelope
// {success, data:[{url,title,description}, ...]}. The api key is sent via
// Authorization: Bearer — never echoed in the request body, so it cannot
// leak through the upstream response the way it could with Tavily.
type FirecrawlProvider struct {
	apiKey     string
	httpClient *http.Client
	endpoint   string
	userAgent  string
}

// FirecrawlOption configures a FirecrawlProvider.
type FirecrawlOption func(*FirecrawlProvider)

// WithFirecrawlHTTPClient overrides the HTTP client (defaults to http.DefaultClient).
func WithFirecrawlHTTPClient(c *http.Client) FirecrawlOption {
	return func(p *FirecrawlProvider) {
		if c != nil {
			p.httpClient = c
		}
	}
}

// WithFirecrawlEndpoint overrides the endpoint URL. Used by tests to point at
// a httptest server.
func WithFirecrawlEndpoint(url string) FirecrawlOption {
	return func(p *FirecrawlProvider) {
		if strings.TrimSpace(url) != "" {
			p.endpoint = url
		}
	}
}

// WithFirecrawlUserAgent overrides the User-Agent header.
func WithFirecrawlUserAgent(ua string) FirecrawlOption {
	return func(p *FirecrawlProvider) {
		if strings.TrimSpace(ua) != "" {
			p.userAgent = ua
		}
	}
}

// NewFirecrawl builds a Firecrawl provider. Returns nil when apiKey is empty
// so the caller can detect "not configured" without a separate Validate call.
func NewFirecrawl(apiKey string, opts ...FirecrawlOption) *FirecrawlProvider {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	p := &FirecrawlProvider{
		apiKey:     apiKey,
		httpClient: http.DefaultClient,
		endpoint:   firecrawlDefaultEndpoint,
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider id.
func (p *FirecrawlProvider) Name() string { return FirecrawlName }

type firecrawlRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type firecrawlResponse struct {
	Success bool   `json:"success"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
	Data    []struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"data"`
}

// Search executes a Firecrawl POST and translates the response into Result list.
func (p *FirecrawlProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	body := firecrawlRequest{
		Query: query,
		Limit: maxResults,
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("firecrawl: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidAPIKey
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, firecrawlMaxBodyBytes))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(bodyBytes)}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, firecrawlMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("firecrawl: read body: %w", err)
	}

	var parsed firecrawlResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, asParseError(fmt.Errorf("firecrawl: decode: %w", err))
	}

	// Firecrawl returns a 200 with success=false on logical failures (quota,
	// invalid query). Surface those as a generic provider_error rather than
	// pretending the response was empty.
	if !parsed.Success && parsed.Error != "" {
		return nil, &HTTPError{Status: resp.StatusCode, Body: parsed.Error}
	}

	out := make([]Result, 0, len(parsed.Data))
	for _, r := range parsed.Data {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, Result{
			URL:     strings.TrimSpace(r.URL),
			Title:   strings.TrimSpace(r.Title),
			Snippet: truncateSnippet(r.Description),
		})
	}
	return out, nil
}
