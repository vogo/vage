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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/vogo/vage/schema"
)

type fetchRequest struct {
	rawURL                string
	maxContentChars       int
	toolAllowedDomains    []string
	requestAllowedDomains []string
	blockedDomains        []string
	respectRobots         bool
	maxResponseBytes      int64
}

type fetchEnvelope struct {
	URL           string   `json:"url"`
	FinalURL      string   `json:"final_url,omitempty"`
	StatusCode    int      `json:"status_code,omitempty"`
	ContentType   string   `json:"content_type,omitempty"`
	Title         string   `json:"title,omitempty"`
	Markdown      string   `json:"markdown,omitempty"`
	Excerpt       string   `json:"excerpt,omitempty"`
	RetrievedAt   string   `json:"retrieved_at"`
	Truncated     bool     `json:"truncated,omitempty"`
	RobotsAllowed bool     `json:"robots_allowed"`
	Warnings      []string `json:"warnings,omitempty"`
	ErrorCode     string   `json:"error_code,omitempty"`
	Message       string   `json:"message,omitempty"`
}

func (t *Tool) fetch(ctx context.Context, req fetchRequest) schema.ToolResult {
	parsed, err := url.Parse(strings.TrimSpace(req.rawURL))
	if err != nil || parsed == nil {
		return errorResult("", "web_fetch: invalid url", "invalid_url", req.rawURL, nil)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errorResult("", "web_fetch: only http and https URLs are supported", "unsupported_scheme", req.rawURL, nil)
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return errorResult("", "web_fetch: url host is required", "invalid_url", req.rawURL, nil)
	}

	// Allow-list semantics: tool's list is a hard cap (intersection). The caller's
	// list can only further narrow it — never widen — so a prompt-injected
	// `allowed_domains` argument cannot escape the operator's safe-list.
	if len(req.toolAllowedDomains) > 0 && !matchesDomain(host, req.toolAllowedDomains) {
		return errorResult("", "web_fetch: domain not in allow-list", "domain_not_allowed", req.rawURL, nil)
	}
	if len(req.requestAllowedDomains) > 0 && !matchesDomain(host, req.requestAllowedDomains) {
		return errorResult("", "web_fetch: domain not in allow-list", "domain_not_allowed", req.rawURL, nil)
	}

	if matchesDomain(host, req.blockedDomains) {
		return errorResult("", "web_fetch: domain is blocked", "domain_blocked", req.rawURL, nil)
	}

	robotsAllowed := true
	if req.respectRobots {
		var robotsErr error
		robotsAllowed, robotsErr = t.checkRobots(ctx, parsed)
		if robotsErr != nil {
			robotsAllowed = true
		}
		if !robotsAllowed {
			return errorResult("", "web_fetch: disallowed by robots.txt", "robots_disallowed", req.rawURL, &fetchEnvelope{
				URL:           req.rawURL,
				RetrievedAt:   time.Now().UTC().Format(time.RFC3339),
				RobotsAllowed: false,
			})
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errorResult("", "web_fetch: "+err.Error(), "request_build_failed", req.rawURL, nil)
	}
	httpReq.Header.Set("User-Agent", t.userAgent)
	httpReq.Header.Set("Accept", "text/html, application/pdf, text/plain, application/json;q=0.9, */*;q=0.1")

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		code := "request_failed"
		if errors.Is(err, errPrivateNetworkBlocked) || strings.Contains(err.Error(), errPrivateNetworkBlocked.Error()) {
			code = "private_network_blocked"
		}
		return errorResult("", "web_fetch: "+err.Error(), code, req.rawURL, &fetchEnvelope{
			URL:           req.rawURL,
			RetrievedAt:   time.Now().UTC().Format(time.RFC3339),
			RobotsAllowed: robotsAllowed,
		})
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, req.maxResponseBytes+1))
	if err != nil {
		return errorResult("", "web_fetch: failed to read response body: "+err.Error(), "body_read_failed", req.rawURL, nil)
	}

	responseTruncated := false
	if int64(len(body)) > req.maxResponseBytes {
		responseTruncated = true
		body = body[:req.maxResponseBytes]
	}

	contentType := normalizeContentType(resp.Header.Get("Content-Type"), body, parsed.Path)
	env := fetchEnvelope{
		URL:           req.rawURL,
		FinalURL:      resp.Request.URL.String(),
		StatusCode:    resp.StatusCode,
		ContentType:   contentType,
		RetrievedAt:   time.Now().UTC().Format(time.RFC3339),
		RobotsAllowed: robotsAllowed,
	}

	if responseTruncated {
		env.Warnings = append(env.Warnings, "response_truncated")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		env.ErrorCode = "http_error"
		env.Message = fmt.Sprintf("web_fetch: unexpected HTTP status %d", resp.StatusCode)
		return jsonResult("", env, true)
	}

	var markdown string
	switch {
	case isHTMLContentType(contentType):
		htmlResult, dynErr := extractHTML(body, req.maxContentChars)
		if dynErr != nil {
			env.ErrorCode = "dynamic_content_requires_browser"
			env.Message = dynErr.Error()
			env.Title = htmlResult.title
			if responseTruncated {
				env.Truncated = true
			}
			return jsonResult("", env, true)
		}
		markdown = htmlResult.markdown
		env.Title = htmlResult.title
		if htmlResult.truncated || responseTruncated {
			env.Truncated = true
		}
		if htmlResult.warning != "" {
			env.Warnings = append(env.Warnings, htmlResult.warning)
		}
	case isPDFContentType(contentType):
		markdown = extractPDFText(body)
		if strings.TrimSpace(markdown) == "" {
			env.ErrorCode = "pdf_text_extraction_failed"
			env.Message = "web_fetch: unable to extract text from PDF"
			return jsonResult("", env, true)
		}
		markdown, env.Truncated = truncateText(markdown, req.maxContentChars)
		env.Title = path.Base(resp.Request.URL.Path)
	case isTextContentType(contentType):
		markdown = strings.TrimSpace(string(body))
		markdown, env.Truncated = truncateText(markdown, req.maxContentChars)
	default:
		env.ErrorCode = "unsupported_content_type"
		env.Message = "web_fetch: unsupported content type"
		return jsonResult("", env, true)
	}

	env.Markdown = markdown
	env.Excerpt = firstExcerpt(markdown, 280)

	return jsonResult("", env, false)
}

func jsonResult(toolCallID string, env fetchEnvelope, isError bool) schema.ToolResult {
	data, _ := json.MarshalIndent(env, "", "  ")
	return schema.ToolResult{
		ToolCallID: toolCallID,
		Content:    []schema.ContentPart{{Type: "text", Text: string(data)}},
		IsError:    isError,
	}
}

func errorResult(toolCallID, message, code, rawURL string, env *fetchEnvelope) schema.ToolResult {
	out := fetchEnvelope{
		URL:         rawURL,
		RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		ErrorCode:   code,
		Message:     message,
	}
	if env != nil {
		out = *env
		out.ErrorCode = code
		out.Message = message
		if out.RetrievedAt == "" {
			out.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	return jsonResult(toolCallID, out, true)
}
