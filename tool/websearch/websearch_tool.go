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
	"strings"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const (
	ToolName        = "web_search"
	toolDescription = "Search the public web by keyword and return a list of result URLs with titles and snippets. The result URLs can be passed to web_fetch for full content. Provider is configured by the operator (Tavily / Brave / SearXNG); the agent does not choose."

	defaultTimeout        = 10 * time.Second
	defaultMaxResults     = 5
	defaultHardMaxResults = 20
	defaultUserAgent      = "vage-web-search/1.0"
	maxQueryRunes         = 1024
	maxTopicRunes         = 64
)

// Tool is the LLM-facing web_search tool. Construction requires a Provider
// via WithProvider — calling New without one returns nil so callers (e.g.
// vv's tool wiring) can detect "not configured" and skip registration.
type Tool struct {
	provider       Provider
	httpClient     *http.Client
	defaultMax     int
	hardMaxResults int
	timeout        time.Duration
	userAgent      string
}

type Option func(*Tool)

// WithProvider injects the search backend. Required.
func WithProvider(p Provider) Option {
	return func(t *Tool) {
		if p != nil {
			t.provider = p
		}
	}
}

// WithHTTPClient overrides the shared HTTP client passed to providers
// that accept one. Provider implementations may still create their own
// client; this Option is only honored by providers that read it via
// SetHTTPClient or a constructor parameter.
func WithHTTPClient(c *http.Client) Option {
	return func(t *Tool) {
		if c != nil {
			t.httpClient = c
		}
	}
}

// WithTimeout sets a per-call timeout applied via context.WithTimeout.
func WithTimeout(d time.Duration) Option {
	return func(t *Tool) {
		if d > 0 {
			t.timeout = d
		}
	}
}

// WithDefaultMaxResults sets the result count used when the LLM omits
// max_results. Bounded by hardMaxResults at call time.
func WithDefaultMaxResults(n int) Option {
	return func(t *Tool) {
		if n > 0 {
			t.defaultMax = n
		}
	}
}

// WithHardMaxResults caps the upper bound a single call may request.
// LLM-supplied max_results above this value is clamped.
func WithHardMaxResults(n int) Option {
	return func(t *Tool) {
		if n > 0 {
			t.hardMaxResults = n
		}
	}
}

// WithUserAgent overrides the default User-Agent header passed by providers
// that respect it.
func WithUserAgent(s string) Option {
	return func(t *Tool) {
		if strings.TrimSpace(s) != "" {
			t.userAgent = s
		}
	}
}

// New builds the Tool. Returns nil when no Provider was supplied so vv-side
// wiring can branch on "not configured" without inspecting fields.
func New(opts ...Option) *Tool {
	t := &Tool{
		defaultMax:     defaultMaxResults,
		hardMaxResults: defaultHardMaxResults,
		timeout:        defaultTimeout,
		userAgent:      defaultUserAgent,
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.provider == nil {
		return nil
	}
	return t
}

func (t *Tool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ToolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search keywords (required). Maximum 1024 characters.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Optional cap on returned result count. Defaults to 5; values above 20 are clamped.",
				},
				"topic": map[string]any{
					"type":        "string",
					"description": "Optional topic hint forwarded to providers that support it (Tavily: 'general' or 'news'). Ignored otherwise.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (t *Tool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return errorResult("", t.provider.Name(), "context_canceled", "web_search: "+err.Error()), nil
		}

		var req struct {
			Query      string `json:"query"`
			MaxResults *int   `json:"max_results"`
			Topic      string `json:"topic"`
		}
		if err := json.Unmarshal([]byte(args), &req); err != nil {
			return errorResult("", t.provider.Name(), "invalid_arguments", "web_search: invalid arguments: "+err.Error()), nil
		}

		query := strings.TrimSpace(req.Query)
		if query == "" {
			return errorResult(query, t.provider.Name(), "empty_query", "web_search: query is required"), nil
		}
		if runes := []rune(query); len(runes) > maxQueryRunes {
			// Echo a runic-truncated prefix so the LLM can recognise its own
			// request without bloating the envelope. 64 runes is enough for a
			// human-meaningful preview while keeping the rejection envelope small.
			const queryEchoMax = 64
			echo := string(runes[:queryEchoMax]) + "…"
			return errorResult(echo, t.provider.Name(), "query_too_long", "web_search: query exceeds 1024 characters"), nil
		}

		maxResults, warnings := t.resolveMaxResults(req.MaxResults)

		callCtx := ctx
		if t.timeout > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, t.timeout)
			defer cancel()
		}

		// Topic is forwarded out-of-band via context so the Provider interface
		// stays single-method. Providers that support it (Tavily) read the
		// value; others ignore it. Cap the length defensively — the LLM should
		// be sending an enum like "general" / "news"; anything longer is dropped
		// so it cannot smuggle large payloads into the upstream request body.
		if topic := strings.TrimSpace(req.Topic); topic != "" && len([]rune(topic)) <= maxTopicRunes {
			callCtx = withTopic(callCtx, topic)
		}

		results, err := t.provider.Search(callCtx, query, maxResults)
		if err != nil {
			return t.translateProviderError(query, err), nil
		}

		if len(results) == 0 {
			warnings = append(warnings, "no_results")
		}

		env := searchEnvelope{
			Query:    query,
			Provider: t.provider.Name(),
			Results:  results,
			Warnings: warnings,
		}
		return jsonResult(env, false), nil
	}
}

func (t *Tool) resolveMaxResults(req *int) (int, []string) {
	if req == nil || *req <= 0 {
		return t.defaultMax, nil
	}
	if *req > t.hardMaxResults {
		return t.hardMaxResults, []string{"max_results_clamped"}
	}
	return *req, nil
}

func (t *Tool) translateProviderError(query string, err error) schema.ToolResult {
	if errors.Is(err, ErrInvalidAPIKey) {
		return errorResult(query, t.provider.Name(), "invalid_api_key", "web_search: provider rejected credentials")
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		env := searchEnvelope{
			Query:      query,
			Provider:   t.provider.Name(),
			ErrorCode:  "provider_error",
			Message:    err.Error(),
			StatusCode: httpErr.Status,
		}
		return jsonResult(env, true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errorResult(query, t.provider.Name(), "timeout", "web_search: request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return errorResult(query, t.provider.Name(), "context_canceled", "web_search: "+err.Error())
	}
	if isParseError(err) {
		return errorResult(query, t.provider.Name(), "parse_failed", "web_search: "+err.Error())
	}
	return errorResult(query, t.provider.Name(), "request_failed", "web_search: "+err.Error())
}

// Register installs the tool on the registry. New(opts...) must produce a
// non-nil Tool — callers that may not have a Provider should skip Register
// after checking New(...) themselves.
func Register(registry *tool.Registry, opts ...Option) error {
	wt := New(opts...)
	if wt == nil {
		return errors.New("websearch: cannot register without a Provider")
	}
	return registry.RegisterIfAbsent(wt.ToolDef(), wt.Handler())
}

type topicCtxKey struct{}

func withTopic(ctx context.Context, topic string) context.Context {
	return context.WithValue(ctx, topicCtxKey{}, topic)
}

// TopicFromContext returns the optional topic hint passed by the LLM, or
// "" when unset. Providers that support topics call this directly.
func TopicFromContext(ctx context.Context) string {
	v, _ := ctx.Value(topicCtxKey{}).(string)
	return v
}

// HTTPClient returns the shared http.Client configured on the Tool, or
// http.DefaultClient when none was supplied. Providers that build requests
// internally should call this instead of constructing their own client so
// vv-side timeouts and proxies stay consistent.
func (t *Tool) HTTPClient() *http.Client {
	if t.httpClient != nil {
		return t.httpClient
	}
	return http.DefaultClient
}

// UserAgent exposes the configured User-Agent for providers.
func (t *Tool) UserAgent() string { return t.userAgent }

type parseError struct{ inner error }

func (p *parseError) Error() string { return p.inner.Error() }
func (p *parseError) Unwrap() error { return p.inner }

// asParseError wraps a JSON / response decoding failure so the Tool layer can
// map it to error_code=parse_failed without sniffing the message.
func asParseError(err error) error { return &parseError{inner: err} }

func isParseError(err error) bool {
	var pe *parseError
	return errors.As(err, &pe)
}
