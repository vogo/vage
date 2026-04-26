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
	// TavilyName is the canonical provider id used in configs and envelopes.
	TavilyName            = "tavily"
	tavilyDefaultEndpoint = "https://api.tavily.com/search"
	tavilyMaxBodyBytes    = 1 << 20 // 1 MiB defensive cap on response read
)

// TavilyProvider implements Provider against api.tavily.com.
type TavilyProvider struct {
	apiKey     string
	httpClient *http.Client
	endpoint   string
	userAgent  string
}

// TavilyOption configures a TavilyProvider.
type TavilyOption func(*TavilyProvider)

// WithTavilyHTTPClient overrides the HTTP client (defaults to http.DefaultClient).
func WithTavilyHTTPClient(c *http.Client) TavilyOption {
	return func(p *TavilyProvider) {
		if c != nil {
			p.httpClient = c
		}
	}
}

// WithTavilyEndpoint overrides the endpoint URL. Used by tests to point at a
// httptest server.
func WithTavilyEndpoint(url string) TavilyOption {
	return func(p *TavilyProvider) {
		if strings.TrimSpace(url) != "" {
			p.endpoint = url
		}
	}
}

// WithTavilyUserAgent overrides the User-Agent header.
func WithTavilyUserAgent(ua string) TavilyOption {
	return func(p *TavilyProvider) {
		if strings.TrimSpace(ua) != "" {
			p.userAgent = ua
		}
	}
}

// NewTavily builds a Tavily provider. Returns nil when apiKey is empty so the
// caller can detect "not configured" without a separate Validate call.
func NewTavily(apiKey string, opts ...TavilyOption) *TavilyProvider {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	p := &TavilyProvider{
		apiKey:     apiKey,
		httpClient: http.DefaultClient,
		endpoint:   tavilyDefaultEndpoint,
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider id.
func (p *TavilyProvider) Name() string { return TavilyName }

type tavilyRequest struct {
	APIKey            string `json:"api_key"`
	Query             string `json:"query"`
	MaxResults        int    `json:"max_results,omitempty"`
	SearchDepth       string `json:"search_depth,omitempty"`
	IncludeAnswer     bool   `json:"include_answer"`
	IncludeRawContent bool   `json:"include_raw_content"`
	Topic             string `json:"topic,omitempty"`
}

type tavilyResponse struct {
	Results []struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

// Search executes a Tavily POST and translates the response into Result list.
func (p *TavilyProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	body := tavilyRequest{
		APIKey:        p.apiKey,
		Query:         query,
		MaxResults:    maxResults,
		SearchDepth:   "basic",
		IncludeAnswer: false,
		Topic:         TopicFromContext(ctx),
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tavily: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidAPIKey
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, tavilyMaxBodyBytes))
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(bodyBytes)}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, tavilyMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("tavily: read body: %w", err)
	}

	var parsed tavilyResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, asParseError(fmt.Errorf("tavily: decode: %w", err))
	}

	out := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
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
