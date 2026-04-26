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
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (t *Tool) checkRobots(ctx context.Context, pageURL *url.URL) (bool, error) {
	robotsURL := &url.URL{
		Scheme: pageURL.Scheme,
		Host:   pageURL.Host,
		Path:   "/robots.txt",
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL.String(), nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("User-Agent", t.userAgent)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return true, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return true, err
	}

	return robotsAllow(string(body), pageURL.Path), nil
}

func robotsAllow(content, pagePath string) bool {
	if pagePath == "" {
		pagePath = "/"
	}

	lines := strings.Split(content, "\n")
	applicable := false
	disallow := false

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			applicable = value == "*" || strings.EqualFold(value, "vv-agent-web-fetch")
		case "allow":
			if applicable && value != "" && strings.HasPrefix(pagePath, value) {
				disallow = false
			}
		case "disallow":
			if applicable && value != "" && strings.HasPrefix(pagePath, value) {
				disallow = true
			}
		}
	}

	return !disallow
}
