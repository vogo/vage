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
	"slices"
	"sort"
	"time"
)

// Endpoint is one routable backend as the router sees it: an identity, ordering
// metadata, and an opaque set of labels. There is no client here and no address
// — the wrapper package owns those, keyed by this endpoint's index.
type Endpoint struct {
	// Alias is the endpoint's operational identity, used for health snapshots
	// and error attribution. An empty alias is derived as "entry-<index>";
	// explicit aliases must be unique across the router.
	Alias string

	// Weight is used by StrategyWeight when it selects an active endpoint. Zero
	// or negative is treated as 1.
	Weight int

	// Tags carry operational attributes (region/tier/workspace) for observation
	// and future strategies. They never participate in routing decisions.
	Tags map[string]string

	// Declares is the endpoint's opaque capability declaration, compared as
	// plain strings against Call.Requires.
	//
	// A nil slice means *undeclared*: the endpoint is unknown, not incapable,
	// and the filter never excludes it. A non-nil slice — including an empty one
	// — is a declaration: the endpoint serves exactly these labels and nothing
	// else. Build it with [Declare] so the distinction is explicit.
	Declares []string

	// Cost optionally declares static pricing for StrategyCost. Nil sorts after
	// priced endpoints.
	Cost *EndpointCost

	// Latency optionally declares a routing latency for StrategyLatency. Nil
	// sorts after endpoints that carry a latency.
	Latency *time.Duration
}

// EndpointCost carries static per-unit pricing used by StrategyCost. Dynamic
// pricing is out of scope; these are fixed routing metadata.
type EndpointCost struct {
	// InputPrice is the cost per input unit (e.g. per 1M input units).
	InputPrice float64
	// OutputPrice is the cost per output unit, scaled by Call.OutputUnits.
	OutputPrice float64
}

// Declare builds an [Endpoint.Declares] value. It always returns a non-nil
// slice, so Declare() with no labels reads as "declares nothing" rather than
// collapsing into the nil "undeclared" case.
func Declare(labels ...string) []string {
	return append(make([]string, 0, len(labels)), labels...)
}

// Endpoint status values reported by [EndpointStat.Status]. Compare against
// these rather than against string literals: the set grew once already, and a
// consumer that tests Status == StatusAvailable to mean "selectable" would
// silently start reading a recovered endpoint as unusable.
const (
	// StatusAvailable is an endpoint a call has succeeded against.
	StatusAvailable = "available"
	// StatusDead is an endpoint out of rotation until its recover time elapses.
	StatusDead = "dead"
	// StatusProbation is an endpoint whose recover time elapsed but which nothing
	// has confirmed since: selectable, and attempted once rather than under the
	// retry policy.
	StatusProbation = "probation"
)

// EndpointStat is an immutable per-endpoint health snapshot returned by
// [Router.Stats].
type EndpointStat struct {
	// Alias is the endpoint's operational identity.
	Alias string
	// Status is one of [StatusAvailable], [StatusDead] or [StatusProbation].
	// Selectable means available or probation; only dead is out of rotation.
	Status string
	// Active reports whether this endpoint is the one currently serving the
	// pool. It is independent of Status: exactly one endpoint is active once the
	// pool has selected one, and a call the active cannot serve is routed
	// elsewhere without moving it.
	Active bool
	// ErrorCount counts how many times the endpoint has been judged dead since
	// its last success. A retry round contributes one, not one per retry.
	ErrorCount int
	// LastError is the failure that judged the endpoint dead, or nil if the
	// endpoint has never failed or has since recovered.
	LastError error
	// ErrorTime is when LastError occurred; the zero value means never/recovered.
	ErrorTime time.Time
}

// AttemptResult reports the outcome of a single endpoint attempt. It is emitted
// to the observer registered via WithAttemptObserver when each attempt finishes:
// for plain calls, when the call returns; for stream-establishing calls, when
// the stream is established or fails to establish. Errors surfaced after
// establishment belong to the stream itself and are not re-reported here — and
// are never retried.
//
// Every real network attempt is reported, so a retried endpoint produces one
// result per attempt under the same alias.
type AttemptResult struct {
	// Alias is the endpoint that was attempted.
	Alias string
	// Success reports whether the attempt succeeded (stream established for
	// stream-establishing calls).
	Success bool
	// Err is the attempt error, nil on success.
	Err error
	// Stream reports whether the attempt established a stream.
	Stream bool
}

// capableIndices returns the endpoint indices that may serve this call, in
// declaration order: those inside Call.Eligible whose declaration satisfies
// every required label. Endpoints that declare nothing are unknown, not
// incapable: they always stay in the candidate list. Health is intentionally
// ignored here — the capability filter runs before health skipping and strategy
// ordering.
func (r *Router) capableIndices(call Call) []int {
	var eligible map[int]bool

	if call.Eligible != nil {
		eligible = make(map[int]bool, len(call.Eligible))
		for _, idx := range call.Eligible {
			eligible[idx] = true
		}
	}

	out := make([]int, 0, len(r.endpoints))

	for i := range r.endpoints {
		if eligible != nil && !eligible[i] {
			continue
		}

		if declares := r.endpoints[i].Declares; declares != nil && !declaresAll(declares, call.Requires) {
			continue
		}

		out = append(out, i)
	}

	return out
}

// declaresAll reports whether every required label appears in the declaration.
func declaresAll(declares, required []string) bool {
	for _, label := range required {
		if !slices.Contains(declares, label) {
			return false
		}
	}

	return true
}

// costKey computes the deterministic routing cost for an endpoint against one
// call. The input volume is a fixed unit and the output volume comes from the
// call; both are constant across endpoints for a given call, so ordering
// reduces to the injected static pricing scaled by the caller's output cap.
func costKey(e *Endpoint, call Call) float64 {
	const inputUnits = 1.0

	outputUnits := call.OutputUnits
	if outputUnits <= 0 {
		outputUnits = 1.0
	}

	return e.Cost.InputPrice*inputUnits + e.Cost.OutputPrice*outputUnits
}

// sortByCost orders candidate indices by ascending static cost. Endpoints
// without pricing metadata sort after priced ones; equal keys tie-break on
// alias so identical inputs always yield an identical order.
func (r *Router) sortByCost(indices []int, call Call) []int {
	sort.Slice(indices, func(a, b int) bool {
		ea, eb := &r.endpoints[indices[a]], &r.endpoints[indices[b]]

		ca, cb := ea.Cost != nil, eb.Cost != nil
		if ca != cb {
			return ca // priced endpoints come first
		}

		if ca { // both priced
			ka, kb := costKey(ea, call), costKey(eb, call)
			if ka != kb {
				return ka < kb
			}
		}

		return ea.Alias < eb.Alias
	})

	return indices
}

// sortByLatency orders candidate indices by ascending injected latency.
// Endpoints without a latency value sort after those with one; equal values
// tie-break on alias.
func (r *Router) sortByLatency(indices []int) []int {
	sort.Slice(indices, func(a, b int) bool {
		ea, eb := &r.endpoints[indices[a]], &r.endpoints[indices[b]]

		la, lb := ea.Latency != nil, eb.Latency != nil
		if la != lb {
			return la // endpoints with latency data come first
		}

		if la && *ea.Latency != *eb.Latency {
			return *ea.Latency < *eb.Latency
		}

		return ea.Alias < eb.Alias
	})

	return indices
}
