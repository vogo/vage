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
	"net/http"
	"strings"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const (
	ToolName        = "web_fetch"
	toolDescription = "Fetch content from a public HTTP(S) URL and return an LLM-friendly summary with extracted Markdown. Supports HTML, plain text, JSON, and basic PDF text extraction. Best-effort robots.txt checking and domain allow/block filters are supported. Dynamic JavaScript-rendered pages are not supported and return an explicit error."

	defaultTimeout          = 15 * time.Second
	defaultMaxResponseBytes = 2 << 20
	defaultMaxContentChars  = 24_000
	defaultUserAgent        = "vv-agent-web-fetch/1.0"
)

type Tool struct {
	httpClient          *http.Client
	maxResponseBytes    int64
	maxContentChars     int
	allowedDomains      []string
	blockedDomains      []string
	userAgent           string
	allowPrivateNetwork bool
}

type Option func(*Tool)

func WithHTTPClient(client *http.Client) Option {
	return func(t *Tool) {
		if client != nil {
			t.httpClient = client
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(t *Tool) {
		if timeout <= 0 {
			return
		}
		client := *t.httpClient
		client.Timeout = timeout
		t.httpClient = &client
	}
}

func WithMaxResponseBytes(n int64) Option {
	return func(t *Tool) {
		if n > 0 {
			t.maxResponseBytes = n
		}
	}
}

func WithMaxContentChars(n int) Option {
	return func(t *Tool) {
		if n > 0 {
			t.maxContentChars = n
		}
	}
}

func WithAllowedDomains(domains ...string) Option {
	return func(t *Tool) { t.allowedDomains = normalizeDomainList(domains) }
}

func WithBlockedDomains(domains ...string) Option {
	return func(t *Tool) { t.blockedDomains = normalizeDomainList(domains) }
}

func WithUserAgent(userAgent string) Option {
	return func(t *Tool) {
		if strings.TrimSpace(userAgent) != "" {
			t.userAgent = userAgent
		}
	}
}

// WithAllowPrivateNetwork lifts the default block on loopback / private /
// link-local / unspecified / multicast destinations. Off by default — turn it
// on only for trusted, internal-only deployments where SSRF risk is acceptable.
func WithAllowPrivateNetwork(allow bool) Option {
	return func(t *Tool) { t.allowPrivateNetwork = allow }
}

func New(opts ...Option) *Tool {
	t := &Tool{
		maxResponseBytes: defaultMaxResponseBytes,
		maxContentChars:  defaultMaxContentChars,
		userAgent:        defaultUserAgent,
	}

	for _, opt := range opts {
		opt(t)
	}

	if t.httpClient == nil {
		t.httpClient = newDefaultHTTPClient(t.allowPrivateNetwork)
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
				"url": map[string]any{
					"type":        "string",
					"description": "Public HTTP or HTTPS URL to fetch.",
				},
				"max_content_chars": map[string]any{
					"type":        "integer",
					"description": "Maximum number of characters to keep in the extracted content. Defaults to the tool's configured limit.",
				},
				"allowed_domains": map[string]any{
					"type":        "array",
					"description": "Optional allow-list of domains. When set, the target URL must match one of them.",
					"items": map[string]any{
						"type": "string",
					},
				},
				"blocked_domains": map[string]any{
					"type":        "array",
					"description": "Optional block-list of domains. Any matching domain is rejected.",
					"items": map[string]any{
						"type": "string",
					},
				},
				"respect_robots": map[string]any{
					"type":        "boolean",
					"description": "Whether to perform a best-effort robots.txt check before fetching. Defaults to true.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}
}

func (t *Tool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return errorResult("", "web_fetch: "+err.Error(), "context_canceled", "", nil), nil
		}

		var reqArgs struct {
			URL             string   `json:"url"`
			MaxContentChars *int     `json:"max_content_chars"`
			AllowedDomains  []string `json:"allowed_domains"`
			BlockedDomains  []string `json:"blocked_domains"`
			RespectRobots   *bool    `json:"respect_robots"`
		}

		if err := json.Unmarshal([]byte(args), &reqArgs); err != nil {
			return errorResult("", "web_fetch: invalid arguments: "+err.Error(), "invalid_arguments", "", nil), nil
		}

		return t.fetch(ctx, fetchRequest{
			rawURL:                reqArgs.URL,
			maxContentChars:       valueOrDefault(reqArgs.MaxContentChars, t.maxContentChars),
			toolAllowedDomains:    t.allowedDomains,
			requestAllowedDomains: normalizeDomainList(reqArgs.AllowedDomains),
			blockedDomains:        normalizeDomainList(append(append([]string(nil), t.blockedDomains...), reqArgs.BlockedDomains...)),
			respectRobots:         reqArgs.RespectRobots == nil || *reqArgs.RespectRobots,
			maxResponseBytes:      t.maxResponseBytes,
		}), nil
	}
}

func Register(registry *tool.Registry, opts ...Option) error {
	wt := New(opts...)
	return registry.RegisterIfAbsent(wt.ToolDef(), wt.Handler())
}
