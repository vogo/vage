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
	"mime"
	"net/http"
	"path"
	"strings"
)

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
}

func dedupeBlocks(blocks []string) string {
	var out []string
	seen := make(map[string]struct{})
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if _, ok := seen[block]; ok {
			continue
		}
		seen[block] = struct{}{}
		out = append(out, block)
	}
	return strings.Join(out, "\n\n")
}

func truncateText(text string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text, false
	}
	if maxChars <= 1 {
		return string(runes[:maxChars]), true
	}
	return strings.TrimSpace(string(runes[:maxChars-1])) + "…", true
}

func firstExcerpt(text string, maxChars int) string {
	text = strings.TrimSpace(collapseWhitespace(text))
	if text == "" {
		return ""
	}
	out, _ := truncateText(text, maxChars)
	return out
}

func normalizeContentType(header string, body []byte, urlPath string) string {
	mediaType, _, err := mime.ParseMediaType(header)
	if err == nil && mediaType != "" {
		return strings.ToLower(mediaType)
	}

	sniffed := strings.ToLower(http.DetectContentType(body))
	if sniffed != "" {
		return sniffed
	}

	switch strings.ToLower(path.Ext(urlPath)) {
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt", ".md":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func isHTMLContentType(contentType string) bool {
	return strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml+xml")
}

func isPDFContentType(contentType string) bool {
	return strings.Contains(contentType, "application/pdf")
}

func isTextContentType(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "application/json")
}

func normalizeDomainList(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(domains))
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		domain = strings.TrimPrefix(domain, ".")
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}

func matchesDomain(host string, domains []string) bool {
	if len(domains) == 0 {
		return false
	}
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func valueOrDefault(v *int, fallback int) int {
	if v == nil || *v <= 0 {
		return fallback
	}
	return *v
}
