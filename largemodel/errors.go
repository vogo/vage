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

package largemodel

import (
	"errors"
	"fmt"
)

// ErrNoAPIKey reports a caller configured without a credential. It surfaces at
// construction time, before any network I/O is attempted.
var ErrNoAPIKey = errors.New("vage: API key is required")

// APIError is a non-2xx response from a model vendor, normalized so the
// governance middlewares can judge failures uniformly. Each protocol's caller
// maps its vendor error onto this type, preserving the HTTP status along with
// whatever machine-readable code and type the vendor supplied.
//
// The vendor's own error remains reachable through Unwrap for callers that
// need the native detail.
type APIError struct {
	// StatusCode is the HTTP status of the failed response.
	StatusCode int

	// Code is the vendor's machine-readable error code, when it supplied one.
	Code string

	// Type is the vendor's error classification (e.g. "rate_limit_error").
	Type string

	// Message is the human-readable message from the vendor.
	Message string

	// Err is the underlying vendor error.
	Err error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("vage: model API error (status %d): %s %s", e.StatusCode, e.Code, e.Message)
}

// Unwrap exposes the underlying vendor error.
func (e *APIError) Unwrap() error { return e.Err }
