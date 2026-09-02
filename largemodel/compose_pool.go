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
	"context"
	"sync"

	"github.com/vogo/vage/largemodel/router"
)

// composePool hands out provider compose clients one caller at a time.
//
// A provider router pool belongs to one conversation and serves it one call at
// a time: a second concurrent call is rejected with router.ErrCallInProgress
// rather than queued. vage shares a Caller across agents that do run in
// parallel, so the caller keeps several pools and lends one out per call,
// which is the router pool's concurrency prescription — one pool per worker.
//
// Each pool learns endpoint health independently. That is the price of
// parallelism here: a backend that dies is discovered once per pool that
// meets it, not once for the caller as a whole.
type composePool[T any] struct {
	// idle holds the pools not currently serving a call. Its capacity is the
	// concurrency limit, so a release never blocks.
	idle chan T

	// mu guards clients, which is both the record of every pool built so far
	// and the count that decides whether another may be built.
	mu      sync.Mutex
	clients []T

	limit int
	build func() (T, error)
}

// newComposePool builds a pool set of at most limit members. One member is
// built immediately so a misconfiguration — an empty endpoint list, a
// duplicate alias, a recover time shorter than the retry backoff — surfaces
// here rather than on the first call.
func newComposePool[T any](limit int, build func() (T, error)) (*composePool[T], error) {
	first, err := build()
	if err != nil {
		return nil, err
	}

	p := &composePool[T]{
		idle:    make(chan T, limit),
		clients: []T{first},
		limit:   limit,
		build:   build,
	}
	p.idle <- first

	return p, nil
}

// acquire borrows a pool, building a new one while the limit allows and
// otherwise waiting for a busy one to be released. A cancelled context ends
// the wait.
func (p *composePool[T]) acquire(ctx context.Context) (T, error) {
	var zero T

	select {
	case c := <-p.idle:
		return c, nil
	default:
	}

	p.mu.Lock()

	// Look again under the lock: a pool released between the check above and
	// this point should be reused rather than answered with a new one. Without
	// this, ordinary contention grows the set to its limit even when the
	// existing pools would have sufficed — and every extra pool is one more
	// place that has to discover a dead endpoint for itself.
	select {
	case c := <-p.idle:
		p.mu.Unlock()

		return c, nil
	default:
	}

	if len(p.clients) < p.limit {
		c, err := p.build()
		if err != nil {
			p.mu.Unlock()

			return zero, err
		}

		p.clients = append(p.clients, c)
		p.mu.Unlock()

		return c, nil
	}

	p.mu.Unlock()

	select {
	case c := <-p.idle:
		return c, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// release returns a pool to the idle set. The channel's capacity is the limit
// and only borrowed pools are released, so the send never blocks.
func (p *composePool[T]) release(c T) {
	select {
	case p.idle <- c:
	default:
	}
}

// snapshot collects one health snapshot per pool built so far. Provider
// clients allow Stats to run concurrently with dispatch, so a borrowed pool is
// included.
func (p *composePool[T]) snapshot(stats func(T) []router.EndpointStat) [][]router.EndpointStat {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([][]router.EndpointStat, 0, len(p.clients))
	for _, c := range p.clients {
		out = append(out, stats(c))
	}

	return out
}

// mergeEndpointStats folds the per-pool snapshots into one view keyed by
// alias, in the order aliases first appear.
//
// The merge states what the caller as a whole knows about a backend. Its error
// count is the sum across pools and its last error is the most recent one any
// pool recorded; Active means at least one pool is currently serving from it.
// The status is the most confident opinion any pool holds, in the order
// available > probation > dead: one pool having succeeded against a backend is
// what the caller knows about it, whatever the pools that have not tried since
// still believe. A merged "dead" therefore means every pool agrees, and a
// merged "probation" that the best any of them can say is that a recover
// window elapsed.
func mergeEndpointStats(snapshots [][]router.EndpointStat) []router.EndpointStat {
	var (
		order  []string
		merged = map[string]*router.EndpointStat{}
		best   = map[string]int{}
	)

	for _, snapshot := range snapshots {
		for i := range snapshot {
			stat := snapshot[i]

			acc, ok := merged[stat.Alias]
			if !ok {
				copied := stat
				copied.ErrorCount = 0
				merged[stat.Alias] = &copied
				order = append(order, stat.Alias)
				acc = &copied
				best[stat.Alias] = statusConfidence(stat.Status)
			}

			best[stat.Alias] = max(best[stat.Alias], statusConfidence(stat.Status))

			acc.ErrorCount += stat.ErrorCount
			acc.Active = acc.Active || stat.Active

			if stat.ErrorTime.After(acc.ErrorTime) {
				acc.LastError = stat.LastError
				acc.ErrorTime = stat.ErrorTime
			}
		}
	}

	out := make([]router.EndpointStat, 0, len(order))

	for _, alias := range order {
		acc := merged[alias]
		acc.Status = statusOfConfidence(best[alias])

		out = append(out, *acc)
	}

	return out
}

// Status confidence levels, ordered from what the pools know least to what
// they know best. The router's status set has grown once already, so an unknown
// value sorts at the bottom rather than being mistaken for a known one.
const (
	confidenceUnknown = iota
	confidenceDead
	confidenceProbation
	confidenceAvailable
)

// statusConfidence ranks one pool's opinion of an endpoint.
func statusConfidence(status string) int {
	switch status {
	case router.StatusAvailable:
		return confidenceAvailable
	case router.StatusProbation:
		return confidenceProbation
	case router.StatusDead:
		return confidenceDead
	default:
		return confidenceUnknown
	}
}

// statusOfConfidence names the merged rank. An unknown rank reports as dead:
// a status vage cannot read is not evidence that a backend is usable.
func statusOfConfidence(rank int) string {
	switch rank {
	case confidenceAvailable:
		return router.StatusAvailable
	case confidenceProbation:
		return router.StatusProbation
	default:
		return router.StatusDead
	}
}

// DefaultEndpointAlias names the single endpoint of a one-endpoint pool, so
// its health snapshots and error attribution read the same as a pool's. The
// single-endpoint constructors assign it when the caller left Alias empty; it
// is exported so an operator reading a health snapshot or a routing error can
// recognise the name nobody chose.
const DefaultEndpointAlias = "default"
