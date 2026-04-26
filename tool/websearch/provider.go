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
	"fmt"
)

// Provider performs a single keyword search and returns the upstream
// result list. Implementations must not mutate the returned slice after
// returning. A nil slice with a nil error is a valid "no results" outcome.
type Provider interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) ([]Result, error)
}

// Result is one search hit. Fields beyond URL are best-effort: providers
// should leave them empty when upstream omits them rather than fabricating.
type Result struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// ErrInvalidAPIKey signals that the upstream rejected the credentials
// (HTTP 401/403). The Tool layer translates it to error_code=invalid_api_key.
var ErrInvalidAPIKey = errors.New("websearch: invalid api key")

// HTTPError carries a non-2xx upstream response so the Tool layer can map
// the status code into the envelope.
type HTTPError struct {
	Status int
	Body   string // truncated upstream body for diagnostics; never logged verbatim
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("websearch: upstream HTTP %d", e.Status)
}
