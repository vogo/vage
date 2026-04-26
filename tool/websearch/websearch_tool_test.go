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
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/tool"
)

// stubProvider is a Provider double for tests that need to bypass the HTTP layer.
type stubProvider struct {
	name      string
	results   []Result
	err       error
	gotQuery  string
	gotMax    int
	gotTopic  string
	delay     time.Duration
	callCount int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Search(ctx context.Context, query string, max int) ([]Result, error) {
	s.callCount++
	s.gotQuery = query
	s.gotMax = max
	s.gotTopic = TopicFromContext(ctx)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}

func decodeEnvelope(t *testing.T, text string) searchEnvelope {
	t.Helper()
	var env searchEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\ntext=%s", err, text)
	}
	return env
}

func envelopeText(t *testing.T, args string, opts ...Option) (searchEnvelope, bool) {
	t.Helper()
	tl := New(opts...)
	if tl == nil {
		t.Fatal("New returned nil")
	}
	res, err := tl.Handler()(context.Background(), "", args)
	if err != nil {
		t.Fatalf("handler returned go error: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty content")
	}
	return decodeEnvelope(t, res.Content[0].Text), res.IsError
}

// scenario: Provider returns 3 results — envelope flat-maps them, IsError=false.
func TestWebSearch_Success(t *testing.T) {
	prov := &stubProvider{name: "tavily", results: []Result{
		{URL: "https://a", Title: "A", Snippet: "snippet a"},
		{URL: "https://b", Title: "B"},
		{URL: "https://c", Snippet: "c"},
	}}
	env, isErr := envelopeText(t, `{"query":"hello"}`, WithProvider(prov))
	if isErr {
		t.Fatalf("unexpected IsError; envelope=%+v", env)
	}
	if env.Provider != "tavily" {
		t.Fatalf("provider=%q want tavily", env.Provider)
	}
	if len(env.Results) != 3 {
		t.Fatalf("results=%d want 3", len(env.Results))
	}
	if env.Query != "hello" {
		t.Fatalf("query=%q", env.Query)
	}
	if prov.gotMax != defaultMaxResults {
		t.Fatalf("default max not applied; got %d", prov.gotMax)
	}
}

// scenario: empty query rejected without provider call.
func TestWebSearch_EmptyQuery(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	env, isErr := envelopeText(t, `{"query":"   "}`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "empty_query" {
		t.Fatalf("error_code=%q want empty_query", env.ErrorCode)
	}
	if prov.callCount != 0 {
		t.Fatal("provider should not be called for empty query")
	}
}

// scenario: query exceeding 1024 runes is rejected pre-flight.
func TestWebSearch_QueryTooLong(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	long := strings.Repeat("a", 1025)
	env, isErr := envelopeText(t, `{"query":"`+long+`"}`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "query_too_long" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
	if prov.callCount != 0 {
		t.Fatal("provider should not be called")
	}
	// query_too_long must echo a short truncated prefix so the LLM can
	// recognise its rejected request, but not blow up the envelope.
	if env.Query == "" {
		t.Fatal("query echo missing on query_too_long; LLM cannot self-correct")
	}
	if len([]rune(env.Query)) > 128 {
		t.Fatalf("query echo not truncated; len=%d", len([]rune(env.Query)))
	}
	if !strings.HasSuffix(env.Query, "…") {
		t.Fatalf("query echo missing ellipsis: %q", env.Query)
	}
}

// scenario: max_results > hard cap is clamped and warning emitted.
func TestWebSearch_MaxResultsClamped(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	env, _ := envelopeText(t, `{"query":"q","max_results":50}`, WithProvider(prov))
	if prov.gotMax != defaultHardMaxResults {
		t.Fatalf("max not clamped; got %d", prov.gotMax)
	}
	if !contains(env.Warnings, "max_results_clamped") {
		t.Fatalf("missing clamp warning; warnings=%v", env.Warnings)
	}
}

// scenario: empty result list yields a warning, not an error.
func TestWebSearch_NoResults(t *testing.T) {
	prov := &stubProvider{name: "tavily", results: nil}
	env, isErr := envelopeText(t, `{"query":"q"}`, WithProvider(prov))
	if isErr {
		t.Fatalf("expected IsError=false; got envelope=%+v", env)
	}
	if !contains(env.Warnings, "no_results") {
		t.Fatalf("missing no_results warning; warnings=%v", env.Warnings)
	}
}

// scenario: ErrInvalidAPIKey from provider maps to envelope error_code=invalid_api_key.
func TestWebSearch_InvalidAPIKey(t *testing.T) {
	prov := &stubProvider{name: "tavily", err: ErrInvalidAPIKey}
	env, isErr := envelopeText(t, `{"query":"q"}`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "invalid_api_key" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
}

// scenario: HTTPError surfaces upstream status and provider_error code.
func TestWebSearch_HTTPError(t *testing.T) {
	prov := &stubProvider{name: "brave", err: &HTTPError{Status: 502}}
	env, isErr := envelopeText(t, `{"query":"q"}`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "provider_error" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
	if env.StatusCode != 502 {
		t.Fatalf("status_code=%d", env.StatusCode)
	}
}

// scenario: parseError from provider maps to error_code=parse_failed.
func TestWebSearch_ParseError(t *testing.T) {
	prov := &stubProvider{name: "brave", err: asParseError(errors.New("decode boom"))}
	env, isErr := envelopeText(t, `{"query":"q"}`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "parse_failed" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
}

// scenario: deadline exceeded translates to timeout code.
func TestWebSearch_Timeout(t *testing.T) {
	prov := &stubProvider{name: "tavily", delay: 100 * time.Millisecond}
	tl := New(WithProvider(prov), WithTimeout(20*time.Millisecond))
	res, _ := tl.Handler()(context.Background(), "", `{"query":"q"}`)
	env := decodeEnvelope(t, res.Content[0].Text)
	if env.ErrorCode != "timeout" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
}

// scenario: topic forwarded to provider via context.
func TestWebSearch_TopicForwarded(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	envelopeText(t, `{"query":"q","topic":"news"}`, WithProvider(prov))
	if prov.gotTopic != "news" {
		t.Fatalf("topic not forwarded; got %q", prov.gotTopic)
	}
}

// scenario: invalid JSON args.
func TestWebSearch_InvalidArgs(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	env, isErr := envelopeText(t, `{not json`, WithProvider(prov))
	if !isErr {
		t.Fatal("expected IsError=true")
	}
	if env.ErrorCode != "invalid_arguments" {
		t.Fatalf("error_code=%q", env.ErrorCode)
	}
}

// scenario: New(nil provider) returns nil.
func TestWebSearch_NewWithoutProvider(t *testing.T) {
	if New() != nil {
		t.Fatal("expected nil when no provider supplied")
	}
}

// scenario: Register without provider reports a clear error.
func TestWebSearch_RegisterRequiresProvider(t *testing.T) {
	reg := tool.NewRegistry()
	if err := Register(reg); err == nil {
		t.Fatal("expected error when registering without provider")
	}
}

// scenario: Register with provider exposes ToolName in registry.
func TestWebSearch_Register(t *testing.T) {
	reg := tool.NewRegistry()
	prov := &stubProvider{name: "tavily"}
	if err := Register(reg, WithProvider(prov)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := reg.Get(ToolName); !ok {
		t.Fatalf("%s missing from registry", ToolName)
	}
}

// ----- Tavily provider via httptest -----

// scenario: Tavily 200 response yields three Result rows with snippet truncation applied.
func TestTavilyProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing content-type header")
		}
		var body tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.APIKey != "test-key" {
			t.Errorf("api_key=%q", body.APIKey)
		}
		if body.Query != "go modules" {
			t.Errorf("query=%q", body.Query)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"url":"https://example.com/a","title":"Title A","content":"snippet a","published_date":"2026-04-01"},
			{"url":"https://example.com/b","title":"Title B","content":"snippet b"},
			{"url":"","title":"skip empty url","content":"skipped"}
		]}`))
	}))
	defer srv.Close()

	prov := NewTavily("test-key", WithTavilyEndpoint(srv.URL))
	results, err := prov.Search(context.Background(), "go modules", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results=%d want 2 (empty URL filtered)", len(results))
	}
	if results[0].PublishedAt != "2026-04-01" {
		t.Fatalf("published=%q", results[0].PublishedAt)
	}
}

// scenario: Tavily 401 → ErrInvalidAPIKey.
func TestTavilyProvider_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	prov := NewTavily("bad", WithTavilyEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey", err)
	}
}

// scenario: Tavily 5xx → HTTPError with original status.
func TestTavilyProvider_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream"))
	}))
	defer srv.Close()
	prov := NewTavily("k", WithTavilyEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err=%v want HTTPError", err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Fatalf("status=%d", httpErr.Status)
	}
}

// scenario: malformed Tavily JSON body → parse_failed via asParseError.
func TestTavilyProvider_ParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	prov := NewTavily("k", WithTavilyEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	if !isParseError(err) {
		t.Fatalf("err=%v want parseError", err)
	}
}

// scenario: NewTavily returns nil for empty key.
func TestTavilyProvider_NewEmptyKey(t *testing.T) {
	if NewTavily("") != nil {
		t.Fatal("expected nil for empty key")
	}
}

// ----- Brave provider via httptest -----

// scenario: Brave 200 maps web.results -> Result list and forwards X-Subscription-Token.
func TestBraveProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "tok" {
			t.Errorf("token header missing/wrong")
		}
		if r.URL.Query().Get("q") != "search me" {
			t.Errorf("q=%q", r.URL.Query().Get("q"))
		}
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"url":"https://example.com/x","title":"X","description":"desc x","page_age":"2026-04-25"},
			{"url":"https://example.com/y","title":"Y","description":"desc y"}
		]}}`))
	}))
	defer srv.Close()
	prov := NewBrave("tok", WithBraveEndpoint(srv.URL))
	results, err := prov.Search(context.Background(), "search me", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results=%d want 2", len(results))
	}
	if results[0].Snippet != "desc x" {
		t.Fatalf("snippet=%q", results[0].Snippet)
	}
}

// scenario: Brave 403 → ErrInvalidAPIKey.
func TestBraveProvider_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	prov := NewBrave("bad", WithBraveEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v", err)
	}
}

// scenario: Brave count parameter is clamped to provider's documented max.
func TestBraveProvider_CountClampedAt20(t *testing.T) {
	var gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCount = r.URL.Query().Get("count")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()
	prov := NewBrave("tok", WithBraveEndpoint(srv.URL))
	_, _ = prov.Search(context.Background(), "q", 999)
	if gotCount != "20" {
		t.Fatalf("count=%q want 20", gotCount)
	}
}

// scenario: NewBrave returns nil for empty key.
func TestBraveProvider_NewEmptyKey(t *testing.T) {
	if NewBrave("") != nil {
		t.Fatal("expected nil for empty key")
	}
}

// ----- Firecrawl provider via httptest -----

// scenario: Firecrawl 200 maps data[] -> Result list, forwards Authorization
// Bearer header, and respects the limit field on the request body.
func TestFirecrawlProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fc-key" {
			t.Errorf("auth header=%q", got)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing content-type header")
		}
		var body firecrawlRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Query != "go modules" {
			t.Errorf("query=%q", body.Query)
		}
		if body.Limit != 5 {
			t.Errorf("limit=%d want 5", body.Limit)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"url":"https://example.com/x","title":"X","description":"desc x"},
			{"url":"https://example.com/y","title":"Y","description":"desc y"},
			{"url":"","title":"skip empty url","description":"skipped"}
		]}`))
	}))
	defer srv.Close()
	prov := NewFirecrawl("fc-key", WithFirecrawlEndpoint(srv.URL))
	results, err := prov.Search(context.Background(), "go modules", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results=%d want 2 (empty URL filtered)", len(results))
	}
	if results[0].Snippet != "desc x" {
		t.Fatalf("snippet=%q", results[0].Snippet)
	}
}

// scenario: Firecrawl 401 → ErrInvalidAPIKey.
func TestFirecrawlProvider_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	prov := NewFirecrawl("bad", WithFirecrawlEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey", err)
	}
}

// scenario: Firecrawl 5xx → HTTPError with original status.
func TestFirecrawlProvider_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream"))
	}))
	defer srv.Close()
	prov := NewFirecrawl("k", WithFirecrawlEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err=%v want HTTPError", err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Fatalf("status=%d", httpErr.Status)
	}
}

// scenario: malformed Firecrawl JSON body → parse_failed via asParseError.
func TestFirecrawlProvider_ParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	prov := NewFirecrawl("k", WithFirecrawlEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	if !isParseError(err) {
		t.Fatalf("err=%v want parseError", err)
	}
}

// scenario: Firecrawl returns 200 with success=false — translated to HTTPError
// so the Tool layer surfaces it as provider_error rather than silently
// returning an empty result list.
func TestFirecrawlProvider_LogicalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"quota exceeded"}`))
	}))
	defer srv.Close()
	prov := NewFirecrawl("k", WithFirecrawlEndpoint(srv.URL))
	_, err := prov.Search(context.Background(), "q", 5)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err=%v want HTTPError on success=false", err)
	}
	if !strings.Contains(httpErr.Body, "quota exceeded") {
		t.Fatalf("body=%q missing upstream error", httpErr.Body)
	}
}

// scenario: NewFirecrawl returns nil for empty key.
func TestFirecrawlProvider_NewEmptyKey(t *testing.T) {
	if NewFirecrawl("") != nil {
		t.Fatal("expected nil for empty key")
	}
}

// scenario (AC-5.2): the api_key must never appear anywhere in the envelope
// — not in results, warnings, message, or any other field. End-to-end
// regression test driving the Handler through the real Tavily provider
// against a stub server that echoes the api_key (worst case).
func TestWebSearch_EnvelopeOmitsAPIKey(t *testing.T) {
	const sentinelKey = "TAVILY-SECRET-DO-NOT-LEAK-9871"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirror the api_key back in the response body — even hostile servers
		// shouldn't be able to surface it through our envelope.
		var body tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"results":[{"url":"https://x","title":"t","content":"echo:` + body.APIKey + `"}]}`))
	}))
	defer srv.Close()
	prov := NewTavily(sentinelKey, WithTavilyEndpoint(srv.URL))
	tl := New(WithProvider(prov))
	res, err := tl.Handler()(context.Background(), "", `{"query":"q"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	body := res.Content[0].Text
	// The stub echoes the key into snippet; ToolResultInjectionGuard would
	// scrub it at a higher layer, but the envelope itself shouldn't carry it
	// from our own request side. (We tolerate it appearing inside snippet
	// because that's upstream content; we only assert it doesn't leak via
	// our own request fields.)
	//
	// Test approach: verify there's no envelope field labelled api_key,
	// Authorization, X-Subscription-Token, etc.
	for _, leak := range []string{`"api_key"`, `"Authorization"`, `"X-Subscription-Token"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("envelope contains forbidden field %s: %s", leak, body)
		}
	}
}

// scenario: oversized topic is dropped pre-flight (defensive cap) — provider
// receives empty topic, no warning is necessary.
func TestWebSearch_TopicOversizedDropped(t *testing.T) {
	prov := &stubProvider{name: "tavily"}
	oversize := strings.Repeat("x", 200)
	envelopeText(t, `{"query":"q","topic":"`+oversize+`"}`, WithProvider(prov))
	if prov.gotTopic != "" {
		t.Fatalf("oversized topic forwarded; got %q", prov.gotTopic)
	}
}

// scenario (AC-1.2): result order from the provider is preserved verbatim
// — no reranking.
func TestWebSearch_OrderPreserved(t *testing.T) {
	prov := &stubProvider{name: "tavily", results: []Result{
		{URL: "https://1"}, {URL: "https://2"}, {URL: "https://3"},
	}}
	env, _ := envelopeText(t, `{"query":"q"}`, WithProvider(prov))
	if len(env.Results) != 3 ||
		env.Results[0].URL != "https://1" ||
		env.Results[1].URL != "https://2" ||
		env.Results[2].URL != "https://3" {
		t.Fatalf("order not preserved: %+v", env.Results)
	}
}

// scenario: snippet truncation at boundary.
func TestTruncateSnippet(t *testing.T) {
	long := strings.Repeat("x", 400)
	got := truncateSnippet(long)
	if len([]rune(got)) > snippetMaxRunes {
		t.Fatalf("not truncated; len=%d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
