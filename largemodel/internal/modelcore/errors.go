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

package modelcore

import (
	"errors"
	"fmt"
)

// ErrEmptyResponse is the single underlying instance behind
// largemodel.ErrEmptyResponse. A codec returns it for a successful vendor
// response that carried nothing to act on, so callers keep routing on the
// root package's sentinel without importing this package.
//
// The message is deliberately the root package's wording: it is user-visible
// through largemodel.ErrEmptyResponse.
var ErrEmptyResponse = errors.New("vage: empty response from model")

// APIError is a vendor failure a codec has already classified: the HTTP status
// it carries (or would carry, for a failure delivered inside a stream) plus
// whatever machine-readable code and type the vendor supplied. Deciding those
// values is vendor knowledge and belongs to the codec; the root package turns
// this into the public *largemodel.APIError without re-reading the wire.
//
// Err is the untouched vendor error, so the native detail stays reachable
// through the public error's Unwrap.
type APIError struct {
	StatusCode int
	Code       string
	Type       string
	Message    string
	Err        error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("vage: model API error (status %d): %s %s", e.StatusCode, e.Code, e.Message)
}

// Unwrap exposes the underlying vendor error.
func (e *APIError) Unwrap() error { return e.Err }
