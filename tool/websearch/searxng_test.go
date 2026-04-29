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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scenario: empty endpoint disables the provider — caller can detect
// "not configured" without a separate Validate call.
func TestNewSearXNG_EmptyEndpointReturnsNil(t *testing.T) {
	if p := NewSearXNG(""); p != nil {
		t.Fatal("NewSearXNG(\"\") = non-nil; want nil")
	}
	if p := NewSearXNG("   "); p != nil {
		t.Fatal("NewSearXNG(whitespace) = non-nil; want nil")
	}
}

// scenario: endpoint without /search is auto-suffixed; one already ending in
// /search is left intact. Operators may configure either form.
func TestNewSearXNG_EndpointNormalisation(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://host", "http://host/search"},
		{"http://host/", "http://host/search"},
		{"http://host/search", "http://host/search"},
		{"http://host/search/", "http://host/search"},
		{"http://host/sub/search", "http://host/sub/search"},
	}
	for _, tc := range cases {
		got := NewSearXNG(tc.in).endpoint
		if got != tc.want {
			t.Errorf("normalise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// scenario: a JSON response with three results is mapped 1:1 into the
// Provider's []Result. Title / snippet trimming and PublishedAt copy through.
func TestSearXNG_Search_Success(t *testing.T) {
	body := `{
		"results": [
			{"url":"https://a","title":"A","content":"snippet a","publishedDate":"2026-01-02"},
			{"url":"https://b","title":"  B  ","content":"  snippet b  "},
			{"url":"https://c","title":"C","content":"c"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("missing format=json; got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "hello" {
			t.Errorf("query forwarded as %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := NewSearXNG(srv.URL)
	results, err := p.Search(context.Background(), "hello", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len=%d, want 3", len(results))
	}
	if results[0].URL != "https://a" || results[0].PublishedAt != "2026-01-02" {
		t.Errorf("result[0]=%+v", results[0])
	}
	if results[1].Title != "B" || !strings.HasPrefix(results[1].Snippet, "snippet b") {
		t.Errorf("trim missed; result[1]=%+v", results[1])
	}
}

// scenario: maxResults > 0 caps the returned slice at the requested size
// even when SearXNG returns more. The Tool layer enforces a hard cap on top.
func TestSearXNG_Search_MaxResultsTrimsResponse(t *testing.T) {
	body := `{"results":[
		{"url":"https://a","title":"A"},
		{"url":"https://b","title":"B"},
		{"url":"https://c","title":"C"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := NewSearXNG(srv.URL)
	results, err := p.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len=%d, want 2 (max applied)", len(results))
	}
}

// scenario: 401 / 403 surface as ErrInvalidAPIKey so the Tool maps to
// invalid_api_key. Mirrors the contract Tavily/Brave/Firecrawl uphold.
func TestSearXNG_Search_AuthErrorMapsToInvalidAPIKey(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()
			_, err := NewSearXNG(srv.URL).Search(context.Background(), "q", 0)
			if !errors.Is(err, ErrInvalidAPIKey) {
				t.Fatalf("err=%v, want ErrInvalidAPIKey", err)
			}
		})
	}
}

// scenario: 429 / 5xx surface as *HTTPError so the Tool layer reports
// provider_error with the upstream status code. Important because
// SearXNG's limiter plugin uses 429 in production.
func TestSearXNG_Search_HTTPErrorCarriesStatus(t *testing.T) {
	for _, status := range []int{429, 500, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "boom")
			}))
			defer srv.Close()
			_, err := NewSearXNG(srv.URL).Search(context.Background(), "q", 0)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("err=%v, want *HTTPError", err)
			}
			if httpErr.Status != status {
				t.Fatalf("status=%d, want %d", httpErr.Status, status)
			}
		})
	}
}

// scenario: malformed JSON body maps to parse_failed via asParseError so the
// Tool layer's translateProviderError can return the right error code.
func TestSearXNG_Search_DecodeFailureMapsToParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[`) // truncated
	}))
	defer srv.Close()

	_, err := NewSearXNG(srv.URL).Search(context.Background(), "q", 0)
	if !isParseError(err) {
		t.Fatalf("err=%v, want parseError", err)
	}
}

// scenario: empty results slice is a valid "no hits" — must not surface as
// an error so the Tool layer can attach the no_results warning instead.
func TestSearXNG_Search_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	results, err := NewSearXNG(srv.URL).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len=%d, want 0", len(results))
	}
}

// scenario: SearXNG returns 200 with an explanatory `message` and zero
// results when an engine fails. Surface that to the Tool as provider_error
// rather than swallowing it as "no hits".
func TestSearXNG_Search_TopLevelMessageBecomesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[],"message":"all engines disabled"}`)
	}))
	defer srv.Close()

	_, err := NewSearXNG(srv.URL).Search(context.Background(), "q", 0)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err=%v, want *HTTPError", err)
	}
	if httpErr.Body != "all engines disabled" {
		t.Errorf("body=%q", httpErr.Body)
	}
}

// scenario: per-call topic from context wins over the configured default
// and only whitelisted SearXNG categories are forwarded — a free-form LLM
// topic ("breaking-news") must be silently dropped to avoid HTTP 400.
func TestSearXNG_Search_CategoriesAndTopicResolution(t *testing.T) {
	cases := []struct {
		name             string
		configCategories string
		ctxTopic         string
		wantCategories   string
	}{
		{"none", "", "", ""},
		{"config only", "general", "", "general"},
		{"topic wins", "general", "news", "news"},
		{"topic dropped if unknown", "general", "breaking-news", ""},
		{"topic comma list filtered", "", "news,unknown,images", "news,images"},
		{"case-fold", "", "NEWS", "news"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCategories string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCategories = r.URL.Query().Get("categories")
				_, _ = io.WriteString(w, `{"results":[]}`)
			}))
			defer srv.Close()

			p := NewSearXNG(srv.URL, WithSearXNGCategories(tc.configCategories))
			ctx := context.Background()
			if tc.ctxTopic != "" {
				ctx = withTopic(ctx, tc.ctxTopic)
			}
			if _, err := p.Search(ctx, "q", 0); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if gotCategories != tc.wantCategories {
				t.Errorf("categories=%q, want %q", gotCategories, tc.wantCategories)
			}
		})
	}
}

// scenario: WithSearXNGAPIKey sends Authorization: Bearer; default omits.
func TestSearXNG_Search_OptionalBearerToken(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	const browserUA = "Mozilla/5.0 (test) Browser/1.0"
	p := NewSearXNG(srv.URL, WithSearXNGAPIKey("secret"), WithSearXNGUserAgent(browserUA))
	if _, err := p.Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization=%q", gotAuth)
	}
	if gotUA != browserUA {
		t.Errorf("UA=%q, want %q", gotUA, browserUA)
	}

	// No-key path: no Authorization header.
	gotAuth, gotUA = "", ""
	p2 := NewSearXNG(srv.URL)
	if _, err := p2.Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("unexpected Authorization=%q", gotAuth)
	}
}

// scenario: language and result-count hints reach the upstream query string.
func TestSearXNG_Search_LanguageAndResultCount(t *testing.T) {
	var gotLang, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.URL.Query().Get("language")
		gotCount = r.URL.Query().Get("results_count")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	p := NewSearXNG(srv.URL, WithSearXNGLanguage("zh-CN"))
	if _, err := p.Search(context.Background(), "q", 7); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotLang != "zh-CN" {
		t.Errorf("language=%q", gotLang)
	}
	if gotCount != "7" {
		t.Errorf("results_count=%q", gotCount)
	}
}

// scenario: provider Name() returns the canonical id used in envelopes.
func TestSearXNG_NameIsCanonical(t *testing.T) {
	if NewSearXNG("http://x").Name() != SearXNGName {
		t.Fatal("Name()")
	}
}
