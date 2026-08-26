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

package router

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoActiveModels reports that a dispatch found nothing to try: every
// endpoint that could have served it was dead and none had recovered yet.
//
// The aggregate failure of a dispatch that did try endpoints is a *MultiError,
// not this sentinel.
var ErrNoActiveModels = errors.New("vage/largemodel/router: no active models available")

// ErrCallInProgress reports that the router is already serving a call. A pool
// belongs to one conversation and serves it one call at a time, so a second
// concurrent call is a usage error rather than something to queue: it is
// rejected immediately, without touching any endpoint or any health state.
//
// A caller that genuinely needs concurrent requests builds one pool per
// concurrent worker; pools are cheap and each keeps its own active endpoint.
var ErrCallInProgress = errors.New("vage/largemodel/router: a call is already in progress on this pool")

// ErrCapabilityNotSatisfied reports that no endpoint declares the labels a call
// required. It is returned before any attempt is made. Match with errors.Is;
// the wrapping CapabilityError names the labels.
var ErrCapabilityNotSatisfied = errors.New("vage/largemodel/router: no endpoint satisfies the required capabilities")

// CapabilityError names the labels a call required and the endpoint aliases
// that were considered. It never triggers a downgrade: the router excludes
// endpoints, it never rewrites what the caller asked for.
type CapabilityError struct {
	// Required lists the unsatisfied labels (e.g. "tools", "vision").
	Required []string
	// Considered lists the aliases of the endpoints that were evaluated.
	Considered []string
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf(
		"vage/largemodel/router: no endpoint satisfies required capabilities [%s] (considered: %s)",
		strings.Join(e.Required, ", "),
		strings.Join(e.Considered, ", "),
	)
}

func (e *CapabilityError) Unwrap() error {
	return ErrCapabilityNotSatisfied
}

// EndpointError wraps a single endpoint's attempt failure, attributing it to a
// stable alias. It unwraps to the underlying error, so errors.Is/As reach the
// original backend error (or any other cause).
type EndpointError struct {
	// Alias is the operational identity of the endpoint that failed.
	Alias string
	// Err is the underlying attempt error.
	Err error
}

func (e *EndpointError) Error() string {
	return fmt.Sprintf("vage/largemodel/router: endpoint %s: %v", e.Alias, e.Err)
}

func (e *EndpointError) Unwrap() error { return e.Err }

// MultiError aggregates the endpoint failures of one dispatch, one entry per
// endpoint in the order they were tried. An endpoint that was retried
// contributes the error it finally failed with, not one entry per retry, so the
// aggregate reads as "these backends were tried and this is how each ended".
// Endpoints serving the same model are distinguished by alias (via
// EndpointError). It implements Go 1.20+ multi-error unwrapping so errors.Is/As
// match any underlying endpoint error.
type MultiError struct {
	Errors []*EndpointError
}

func (e *MultiError) Error() string {
	if len(e.Errors) == 0 {
		return "vage/largemodel/router: all endpoints failed"
	}

	var b strings.Builder

	b.WriteString("vage/largemodel/router: all endpoints failed: ")

	for i, ee := range e.Errors {
		if i > 0 {
			b.WriteString("; ")
		}

		fmt.Fprintf(&b, "%s: %v", ee.Alias, ee.Err)
	}

	return b.String()
}

// Unwrap returns the endpoint errors for Go 1.20+ multi-error unwrapping.
func (e *MultiError) Unwrap() []error {
	errs := make([]error, len(e.Errors))
	for i := range e.Errors {
		errs[i] = e.Errors[i]
	}

	return errs
}
