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

package webfetch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

func TestWebFetchTool_HTMLSuccess(t *testing.T) {
	wt := New(WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/robots.txt":
			return stringResponse(req, http.StatusOK, "text/plain", "User-agent: *\nAllow: /\n"), nil
		default:
			return stringResponse(req, http.StatusOK, "text/html; charset=utf-8", `<!doctype html><html><head><title>Example Doc</title></head><body><main><h1>Heading</h1><p>Hello <a href="https://example.com">world</a>.</p></main></body></html>`), nil
		}
	})))
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://example.test/article"}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.Title != "Example Doc" {
		t.Fatalf("title = %q, want Example Doc", env.Title)
	}
	if !strings.Contains(env.Markdown, "# Example Doc") {
		t.Fatalf("markdown missing title header: %q", env.Markdown)
	}
	if !strings.Contains(env.Markdown, "[world](https://example.com)") {
		t.Fatalf("markdown missing link conversion: %q", env.Markdown)
	}
	if !env.RobotsAllowed {
		t.Fatal("robots_allowed = false, want true")
	}
}

func TestWebFetchTool_DomainFilters(t *testing.T) {
	wt := New()
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://example.com","allowed_domains":["internal.example.com"]}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "domain_not_allowed" {
		t.Fatalf("error_code = %q, want domain_not_allowed", env.ErrorCode)
	}
}

func TestWebFetchTool_RobotsDenied(t *testing.T) {
	wt := New(WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/robots.txt" {
			return stringResponse(req, http.StatusOK, "text/plain", "User-agent: *\nDisallow: /\n"), nil
		}
		return stringResponse(req, http.StatusOK, "text/plain", "hello"), nil
	})))
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://example.test/private"}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "robots_disallowed" {
		t.Fatalf("error_code = %q, want robots_disallowed", env.ErrorCode)
	}
	if env.RobotsAllowed {
		t.Fatal("robots_allowed = true, want false")
	}
}

func TestWebFetchTool_DynamicPageRejected(t *testing.T) {
	body := `<!doctype html><html><head><title>SPA</title><script>a</script><script>b</script><script>c</script><script>d</script><script>e</script></head><body><div id="app"></div><noscript>Please enable JavaScript to run this app.</noscript></body></html>`
	wt := New(WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/robots.txt" {
			return stringResponse(req, http.StatusNotFound, "text/plain", "missing"), nil
		}
		return stringResponse(req, http.StatusOK, "text/html", body), nil
	})))
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://example.test/spa"}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "dynamic_content_requires_browser" {
		t.Fatalf("error_code = %q, want dynamic_content_requires_browser", env.ErrorCode)
	}
}

func TestWebFetchTool_PDFExtraction(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj\n<<>>\nstream\nBT\n(Hello PDF World) Tj\nET\nendstream\nendobj\n%%EOF")
	wt := New(WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/robots.txt" {
			return stringResponse(req, http.StatusNotFound, "text/plain", "missing"), nil
		}
		return bytesResponse(req, http.StatusOK, "application/pdf", pdf), nil
	})))
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://example.test/sample.pdf"}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	env := decodeEnvelope(t, toolkit.ResultText(result))
	if !strings.Contains(env.Markdown, "Hello PDF World") {
		t.Fatalf("markdown = %q, want PDF text", env.Markdown)
	}
}

func TestWebFetchTool_RequestAllowedDomainsCannotWiden(t *testing.T) {
	called := false
	wt := New(
		WithAllowedDomains("safe.example.com"),
		WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
			called = true
			return stringResponse(req, http.StatusOK, "text/plain", "hello"), nil
		})),
	)
	result, err := wt.Handler()(context.Background(), "", `{"url":"https://attacker.example/"}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}
	if called {
		t.Fatal("transport should not be invoked when domain blocked by tool allow-list")
	}
	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "domain_not_allowed" {
		t.Fatalf("error_code = %q, want domain_not_allowed", env.ErrorCode)
	}

	// Even when caller injects allowed_domains, tool's allow-list still gates.
	result2, err := wt.Handler()(context.Background(), "", `{"url":"https://attacker.example/","allowed_domains":["attacker.example"]}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result2.IsError {
		t.Fatal("expected IsError=true even with caller-supplied allow-list")
	}
	env2 := decodeEnvelope(t, toolkit.ResultText(result2))
	if env2.ErrorCode != "domain_not_allowed" {
		t.Fatalf("error_code = %q, want domain_not_allowed", env2.ErrorCode)
	}
	if called {
		t.Fatal("transport should still not be invoked")
	}
}

func TestWebFetchTool_RequestAllowedDomainsCanNarrow(t *testing.T) {
	wt := New(
		WithAllowedDomains("a.example.com", "b.example.com"),
		WithHTTPClient(newTestClient(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/robots.txt" {
				return stringResponse(req, http.StatusNotFound, "text/plain", "missing"), nil
			}
			return stringResponse(req, http.StatusOK, "text/plain", "hi"), nil
		})),
	)

	// Caller narrows to only b.example.com → request to a.example.com is rejected.
	result, _ := wt.Handler()(context.Background(), "", `{"url":"https://a.example.com/x","allowed_domains":["b.example.com"]}`)
	if !result.IsError {
		t.Fatalf("expected narrow allow-list to reject host outside it")
	}
	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "domain_not_allowed" {
		t.Fatalf("error_code = %q, want domain_not_allowed", env.ErrorCode)
	}

	// Same caller-narrowed list still allows hosts inside the intersection.
	ok, _ := wt.Handler()(context.Background(), "", `{"url":"https://b.example.com/x","allowed_domains":["b.example.com"]}`)
	if ok.IsError {
		t.Fatalf("expected success when host is in tool ∩ caller allow-list, got: %s", toolkit.ResultText(ok))
	}
}

func TestWebFetchTool_PrivateNetworkBlockedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()

	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || isPublicIP(ip) {
		t.Skipf("httptest server bound to non-private address %q; skip", host)
	}

	wt := New()
	result, err := wt.Handler()(context.Background(), "", `{"url":"`+srv.URL+`","respect_robots":false}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected SSRF guard to block %s", srv.URL)
	}
	env := decodeEnvelope(t, toolkit.ResultText(result))
	if env.ErrorCode != "private_network_blocked" {
		t.Fatalf("error_code = %q, want private_network_blocked", env.ErrorCode)
	}
}

func TestWebFetchTool_PrivateNetworkAllowedWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("internal-ok"))
	}))
	defer srv.Close()

	wt := New(WithAllowPrivateNetwork(true))
	result, err := wt.Handler()(context.Background(), "", `{"url":"`+srv.URL+`","respect_robots":false}`)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success when private network allowed, got: %s", toolkit.ResultText(result))
	}
	env := decodeEnvelope(t, toolkit.ResultText(result))
	if !strings.Contains(env.Markdown, "internal-ok") {
		t.Fatalf("markdown = %q, want internal-ok", env.Markdown)
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"172.16.0.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"::1", false},
		{"fe80::1", false},
		{"2001:4860:4860::8888", true},
	}
	for _, tc := range cases {
		got := isPublicIP(net.ParseIP(tc.ip))
		if got != tc.want {
			t.Errorf("isPublicIP(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestWebFetchTool_Register(t *testing.T) {
	reg := tool.NewRegistry()
	if err := Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, ok := reg.Get(ToolName); !ok {
		t.Fatalf("%s not found in registry", ToolName)
	}
}

func decodeEnvelope(t *testing.T, text string) fetchEnvelope {
	t.Helper()

	var env fetchEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\ntext=%s", err, text)
	}
	return env
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func stringResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return bytesResponse(req, status, contentType, []byte(body))
}

func bytesResponse(req *http.Request, status int, contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    req,
	}
}
