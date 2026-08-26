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
	"context"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"sync"
	"time"
)

const (
	// defaultRetryBase is the first retry's wait; each further retry doubles it.
	defaultRetryBase = 500 * time.Millisecond
	// defaultMaxRetries is the number of retries *after* the first attempt, so an
	// endpoint is attempted at most four times by default and the worst-case
	// synchronous wait is 500ms × (2^3 − 1) = 3.5s.
	defaultMaxRetries = 3
	// defaultRecoverTime is how long a dead endpoint stays out of rotation. It
	// must exceed retryBase × 2^maxRetries; the default clears that bound by an
	// order of magnitude.
	defaultRecoverTime = 60 * time.Second
)

// noActive is the sentinel index meaning "the pool has not selected an active
// endpoint yet", which is also the state a retired active leaves behind.
const noActive = -1

// Router is the protocol-neutral routing core: it owns endpoint identity, the
// pool's active endpoint, selection strategies, health state and failure
// attribution, and nothing else. It never sees a request or a response — a
// wrapper package binds the types and hands [Dispatch] a closure that performs
// one attempt against one endpoint.
//
// That boundary is the whole point: routing *mechanism* is shared, protocol
// *semantics* are not.
//
// # The active endpoint
//
// A pool serves every call from one endpoint — its *active* endpoint — and
// re-selects only when it has none yet or the current one is judged dead. A
// strategy therefore decides who serves the pool, not who serves a call: it runs
// at selection time, not on every dispatch. When a call must select or fail over,
// it freezes the strategy ordering once for that selection and walks it rather
// than re-drawing after each death.
//
// # One call at a time
//
// A pool belongs to one conversation and serves it one call at a time. A second
// call arriving while one is in flight is rejected with [ErrCallInProgress]
// rather than queued: concurrency here is a usage error, not a load to smooth
// out. A caller that needs parallel requests builds one pool per worker.
type Router struct {
	endpoints        []Endpoint
	health           []*endpointHealth
	strategy         Strategy
	retryBase        time.Duration
	maxRetries       int
	recoverTime      time.Duration
	attemptObservers []func(AttemptResult)
	nowFunc          func() time.Time
	// waitFunc blocks for d or until ctx ends, returning ctx.Err() if it does.
	// It is a field so tests can assert the retry wait sequence without sleeping.
	waitFunc func(ctx context.Context, d time.Duration) error

	// callSlot serialises dispatches: it holds exactly one token, so a router
	// serves one call at a time. It is a channel rather than a mutex because the
	// claim is a *try*, not a wait — a second concurrent call is rejected with
	// ErrCallInProgress rather than queued.
	callSlot chan struct{}

	// mu protects rng, active and generation — everything about *which* endpoint
	// serves the pool. It is held only for those decisions, never across an
	// attempt or a retry wait, so Stats stays callable while a call is in flight.
	mu  sync.Mutex
	rng *rand.Rand
	// active is the index of the endpoint currently serving the pool, or
	// noActive. generation advances on every transition of active (both
	// retirement and commit), which is what makes a switch conditional: a caller
	// that observed generation g may only retire the active it saw at g.
	active     int
	generation uint64
	// commits counts how many endpoints have been committed as the pool's active
	// one over its lifetime. One commit is the initial selection; each further
	// commit is a switch. Call-slot serialisation keeps concurrent dispatches
	// from racing on active; the generation on each pick still makes retirement
	// conditional on the observation that produced it.
	commits uint64
}

// Option configures a Router. The same options serve every wrapper package, so
// a pool's operational behaviour is described once regardless of the protocol
// its endpoints speak.
type Option func(*Router)

// WithRetryPolicy sets the in-call retry policy for the active endpoint: base is
// the wait before the first retry and each further retry doubles it, so retry k
// waits base × 2^(k-1) and an endpoint is attempted at most 1 + maxRetries
// times. Exhausting them is what judges an endpoint dead.
//
// The waits are synchronous — they block the caller's request for up to
// base × (2^maxRetries − 1) in total — and interruptible: a cancelled context
// ends the wait and the call immediately. This is the one place in this module
// where a retry lives; provider packages stay retry-free.
//
// The policy does not apply to an endpoint on probation; see [WithRecoverTime].
func WithRetryPolicy(base time.Duration, maxRetries int) Option {
	return func(r *Router) {
		r.retryBase = base
		r.maxRetries = maxRetries
	}
}

// WithRecoverTime sets how long a dead endpoint stays out of rotation. When it
// elapses the endpoint is a candidate again — that is all: recovery never
// displaces a healthy active endpoint, so an endpoint coming back causes no
// switch on its own.
//
// It comes back on probation: no probe request is issued, so the clock is no
// evidence the endpoint works, and the first call routed to it is attempted once
// rather than under the retry policy. A success promotes it; a failure judges it
// dead again and restarts the window. [Router.Stats] reports the interim as
// [StatusProbation].
//
// It must be strictly greater than base × 2^maxRetries, so a recovered endpoint
// is never re-selected inside the same backoff scale it just failed through.
// [NewRouter] rejects a configuration that violates this.
func WithRecoverTime(d time.Duration) Option {
	return func(r *Router) {
		r.recoverTime = d
	}
}

// WithAttemptObserver registers a callback invoked when each endpoint attempt
// finishes (see AttemptResult), including every retry against the same endpoint.
// Multiple observers may be registered. Observers run synchronously on the
// request path under no internal lock: they must be fast and must not call
// blocking Router operations.
func WithAttemptObserver(fn func(AttemptResult)) Option {
	return func(r *Router) {
		if fn != nil {
			r.attemptObservers = append(r.attemptObservers, fn)
		}
	}
}

// NewRouter creates a Router over the given endpoints, in the order they are
// declared. The index of an endpoint in this slice is its identity for the rest
// of its life: [Dispatch] hands that index back to the attempt closure, which is
// how a wrapper finds the client and the model name that belong to it.
//
// Endpoints are copied before aliases are resolved, so the caller's slice and
// its elements are never written back to. The retry policy and recover time are
// validated here rather than at dispatch: a pool whose recovery window is
// shorter than its own backoff is a configuration error, not a runtime one.
func NewRouter(strategy Strategy, endpoints []Endpoint, opts ...Option) (*Router, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("vage/largemodel/router: at least one endpoint is required")
	}

	owned := slices.Clone(endpoints)

	if err := resolveAliases(owned); err != nil {
		return nil, err
	}

	health := make([]*endpointHealth, len(owned))
	for i := range health {
		health[i] = newEndpointHealth()
	}

	r := &Router{
		endpoints:   owned,
		health:      health,
		strategy:    strategy,
		retryBase:   defaultRetryBase,
		maxRetries:  defaultMaxRetries,
		recoverTime: defaultRecoverTime,
		nowFunc:     time.Now,
		waitFunc:    waitFor,
		callSlot:    make(chan struct{}, 1),
		rng:         newRand(time.Now().UnixNano()),
		active:      noActive,
	}

	for _, opt := range opts {
		opt(r)
	}

	if err := r.validateTiming(); err != nil {
		return nil, err
	}

	return r, nil
}

// validateTiming rejects a retry/recovery configuration that cannot mean what it
// says: non-positive durations, a negative retry count, or a recovery window
// that does not outlast the backoff scale the retries themselves reach.
func (r *Router) validateTiming() error {
	if r.retryBase <= 0 {
		return fmt.Errorf("vage/largemodel/router: retry base must be positive, got %v", r.retryBase)
	}

	if r.maxRetries < 0 {
		return fmt.Errorf("vage/largemodel/router: max retries must not be negative, got %d", r.maxRetries)
	}

	if r.recoverTime <= 0 {
		return fmt.Errorf("vage/largemodel/router: recover time must be positive, got %v", r.recoverTime)
	}

	bound, ok := backoffBound(r.retryBase, r.maxRetries)
	if !ok {
		return fmt.Errorf(
			"vage/largemodel/router: retry policy overflows: base %v doubled %d times exceeds the maximum duration",
			r.retryBase, r.maxRetries,
		)
	}

	if r.recoverTime <= bound {
		return fmt.Errorf(
			"vage/largemodel/router: recover time %v must be greater than base × 2^maxRetries (%v × 2^%d = %v)",
			r.recoverTime, r.retryBase, r.maxRetries, bound,
		)
	}

	return nil
}

// backoffBound returns base × 2^maxRetries, reporting false if that overflows a
// time.Duration. It is the scale the retry waits reach, and the lower bound the
// recover time must clear.
func backoffBound(base time.Duration, maxRetries int) (time.Duration, bool) {
	bound := base

	for range maxRetries {
		if bound > math.MaxInt64/2 {
			return 0, false
		}

		bound *= 2
	}

	return bound, true
}

// waitFor blocks for d, or until ctx ends — in which case it returns ctx.Err()
// so a cancelled caller never pays for the rest of a retry wait.
func waitFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// resolveAliases guarantees every endpoint has a non-empty, unique alias so all
// health snapshots and errors are stably addressable. An empty alias becomes
// "entry-<index>"; a wrapper that derives readable aliases from its own metadata
// does so before calling NewRouter and leaves the rest empty. Explicit aliases
// must not collide with any other alias, derived or explicit. It writes to the
// slice it is given, which is always the router's own copy — never the caller's.
func resolveAliases(endpoints []Endpoint) error {
	seen := make(map[string]int, len(endpoints))

	for i := range endpoints {
		alias := endpoints[i].Alias
		if alias == "" {
			alias = fmt.Sprintf("entry-%d", i)
			endpoints[i].Alias = alias
		}

		if prev, dup := seen[alias]; dup {
			return fmt.Errorf("vage/largemodel/router: duplicate alias %q at endpoints %d and %d", alias, prev, i)
		}

		seen[alias] = i
	}

	return nil
}

// Call describes one dispatch to the router in neutral terms. A wrapper derives
// every field from its own native request; none of them carries protocol
// meaning here.
type Call struct {
	// Requires lists the opaque labels an endpoint must declare to serve this
	// call. The router compares them as strings against Endpoint.Declares and
	// attaches no meaning to either side. An endpoint that declares nothing is
	// unknown, not incapable, and is never excluded.
	Requires []string

	// Eligible optionally restricts routing to these endpoint indices. Nil means
	// every endpoint takes part. It exists for facts about the caller's backend
	// object rather than about the protocol — for example a wrapper whose entry
	// clients do not all implement the method set this call needs.
	Eligible []int

	// OutputUnits scales EndpointCost.OutputPrice when ordering by StrategyCost.
	// A wrapper sets it from whatever its protocol calls an output cap; zero or
	// negative counts as one unit. It never reaches any endpoint.
	OutputUnits float64

	// Stream reports whether the attempt establishes a stream. It is carried
	// through to AttemptResult for observation and changes no routing decision.
	Stream bool
}

// Dispatch serves one call from the pool's active endpoint and returns its
// value. The chain is: capability filter → reuse the active endpoint when it
// can serve → otherwise freeze the strategy ordering once and walk it → attempt
// with in-call exponential retries → on exhaustion, mark dead and continue the
// walk. An endpoint whose recover time has just elapsed is on probation and
// takes a single attempt instead of a retry round.
//
// The strategy runs only when a pick cannot reuse a healthy, capable active
// endpoint — the same moments that commit or temporarily replace it. Successful
// reuse therefore does not advance Random / Weight RNG.
//
// attempt is invoked with the index of the endpoint to try; it owns everything
// protocol-shaped — building the per-endpoint request, calling the backend, and
// returning its value. The router only learns whether the attempt failed, and
// classifies that failure structurally (see classifyFailure).
//
// Failures are collected one per endpoint — a retry round contributes its final
// error, not one entry per retry — and returned as a *MultiError; having no
// candidate to try at all yields ErrNoActiveModels; an unsatisfiable Requires
// set yields a *CapabilityError before any attempt is made.
//
// A router serves one dispatch at a time: a call arriving while another is in
// flight is rejected with ErrCallInProgress, having touched no endpoint and no
// health state. The capability filter runs before that check, so an unservable
// call is reported as such rather than as a busy pool.
func Dispatch[T any](
	ctx context.Context,
	r *Router,
	call Call,
	attempt func(ctx context.Context, endpoint int) (T, error),
) (T, error) {
	var zero T

	// The capability filter runs before health, the active endpoint and the
	// strategy alike. A call whose required labels no endpoint declares fails
	// fast, before any attempt — and before the call slot is claimed, so an
	// unservable call is never reported as a busy pool.
	capable := r.capableIndices(call)
	if len(call.Requires) > 0 && len(capable) == 0 {
		return zero, &CapabilityError{Required: slices.Clone(call.Requires), Considered: r.Aliases()}
	}

	if err := r.acquire(ctx); err != nil {
		return zero, err
	}
	defer r.release()

	// ordering is the strategy's full sequence for this call, frozen lazily the
	// first time a pick cannot reuse the active endpoint. Successful reuse of a
	// healthy active must not run the strategy (and therefore must not advance
	// Random / Weight RNG).
	var ordering []int

	var errs []*EndpointError

	tried := make(map[int]bool, len(capable))

	for {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		choice, ok := r.reuseActive(capable, tried)
		if !ok {
			if ordering == nil {
				ordering = r.freezeOrdering(call, capable)
			}

			choice, ok = r.pickFromOrdering(ordering, tried)
			if !ok {
				break
			}
		}

		tried[choice.endpoint] = true

		result, failure, cancelled := runAttempts(ctx, r, call, choice, attempt)
		if cancelled != nil {
			return zero, cancelled
		}

		if failure == nil {
			return result, nil
		}

		// The endpoint is out: record why, and if it was the one serving the pool,
		// retire it so the next choice commits a replacement.
		r.health[choice.endpoint].markDead(failure, r.nowFunc())

		if choice.serving {
			r.retireActive(choice.endpoint, choice.generation)
		}

		errs = append(errs, &EndpointError{Alias: r.endpoints[choice.endpoint].Alias, Err: failure})
	}

	if len(errs) == 0 {
		return zero, ErrNoActiveModels
	}

	return zero, &MultiError{Errors: errs}
}

// runAttempts drives one endpoint through its whole retry round. It returns
// either a value, or the final failure that judges the endpoint dead, or the
// context error that ends the call without touching health.
//
// Retry k waits retryBase × 2^(k-1) beforehand; a credential failure skips the
// retries entirely, and so does an endpoint on probation. Every real network
// attempt is observed, including retries.
func runAttempts[T any](
	ctx context.Context,
	r *Router,
	call Call,
	pick endpointPick,
	attempt func(ctx context.Context, endpoint int) (T, error),
) (value T, failure, cancelled error) {
	var zero T

	endpoint := pick.endpoint
	alias := r.endpoints[endpoint].Alias

	// An endpoint on probation gets one attempt, not a retry round.
	maxRetries := r.maxRetries
	if pick.probation {
		maxRetries = 0
	}

	for retry := 0; ; retry++ {
		if ctx.Err() != nil {
			return zero, nil, ctx.Err()
		}

		result, err := attempt(ctx, endpoint)
		if err == nil {
			r.observe(AttemptResult{Alias: alias, Success: true, Stream: call.Stream})
			r.health[endpoint].markSuccess()

			return result, nil, nil
		}

		r.observe(AttemptResult{Alias: alias, Success: false, Err: err, Stream: call.Stream})

		// Cancellation never poisons health and never retries: the endpoint did
		// not misbehave, the caller went away.
		if ctx.Err() != nil {
			return zero, nil, ctx.Err()
		}

		if classifyFailure(err) == outcomeCredential || retry >= maxRetries {
			return zero, err, nil
		}

		if waitErr := r.waitFunc(ctx, r.retryBase<<retry); waitErr != nil {
			return zero, nil, waitErr
		}
	}
}

// endpointPick is the outcome of one selection: which endpoint to attempt,
// whether its candidacy is provisional, and whether it is the endpoint currently
// serving the pool (in which case failing it retires the active at the
// generation observed here). A pick that is not serving is temporary — made
// because the active cannot satisfy this call's capabilities — and must never
// rewrite the pool's active endpoint.
type endpointPick struct {
	endpoint   int
	generation uint64
	// probation marks an endpoint that is only selectable because its recover
	// time elapsed. It gets one attempt rather than a retry round.
	probation bool
	serving   bool
}

// reuseActive returns the pool's active endpoint when it is selectable, capable
// of this call, and not yet tried. It never runs the strategy.
func (r *Router) reuseActive(capable []int, tried map[int]bool) (endpointPick, bool) {
	now := r.nowFunc()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active == noActive || tried[r.active] || !slices.Contains(capable, r.active) {
		return endpointPick{}, false
	}

	usable, probation := r.health[r.active].selectable(now, r.recoverTime)
	if !usable {
		return endpointPick{}, false
	}

	return endpointPick{endpoint: r.active, generation: r.generation, probation: probation, serving: true}, true
}

// pickFromOrdering returns the next endpoint from a frozen strategy ordering.
//
//   - When there is no usable active endpoint, the pick is committed as the
//     pool's active.
//   - When the active endpoint is healthy but cannot serve this call (or was
//     already tried), the pick is temporary — the pool's active is left alone.
func (r *Router) pickFromOrdering(ordering []int, tried map[int]bool) (endpointPick, bool) {
	now := r.nowFunc()

	r.mu.Lock()
	defer r.mu.Unlock()

	// activeUsable asks only about health, never about this call's capabilities:
	// a healthy active endpoint keeps serving the pool even when the current call
	// has to be routed around it.
	activeUsable := r.active != noActive && r.health[r.active].available(now, r.recoverTime)

	pick, probation, ok := nextInOrdering(ordering, tried, func(idx int) (bool, bool) {
		return r.health[idx].selectable(now, r.recoverTime)
	})
	if !ok {
		return endpointPick{}, false
	}

	if activeUsable {
		return endpointPick{endpoint: pick, generation: r.generation, probation: probation, serving: false}, true
	}

	r.active = pick
	r.generation++
	r.commits++

	return endpointPick{endpoint: pick, generation: r.generation, probation: probation, serving: true}, true
}

// nextInOrdering returns the first index in ordering that is not yet tried and
// that selectable reports as still selectable, along with whether that
// candidacy is provisional.
func nextInOrdering(
	ordering []int, tried map[int]bool, selectable func(int) (bool, bool),
) (idx int, probation, ok bool) {
	for _, candidate := range ordering {
		if tried[candidate] {
			continue
		}

		if usable, provisional := selectable(candidate); usable {
			return candidate, provisional, true
		}
	}

	return 0, false, false
}

// acquire claims the router's single call slot, or reports ErrCallInProgress if
// another call already holds it. It never waits: a pool serves one conversation,
// so a second concurrent call is a usage error, and queueing would only convert
// that error into unbounded latency.
func (r *Router) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case r.callSlot <- struct{}{}:
		return nil
	default:
		return ErrCallInProgress
	}
}

// release gives the call slot back.
func (r *Router) release() { <-r.callSlot }

// retireActive gives up the pool's active endpoint, but only if it is still the
// endpoint and generation the caller observed. That ties retirement to the pick
// that produced it: a stale retire cannot clear an active that a later pick has
// already replaced. With call-slot serialisation this is defensive; it still
// keeps active-state transitions conditional on the observed generation.
func (r *Router) retireActive(endpoint int, generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active == endpoint && r.generation == generation {
		r.active = noActive
		r.generation++
	}
}

// observe fans an attempt result out to every registered observer. It holds no
// internal lock; observers must be fast and non-blocking.
func (r *Router) observe(result AttemptResult) {
	for _, fn := range r.attemptObservers {
		fn(result)
	}
}

// Aliases returns every endpoint alias in declaration order. A wrapper uses it
// to name the endpoints it considered in an error it raises itself, before
// dispatch begins.
func (r *Router) Aliases() []string {
	aliases := make([]string, len(r.endpoints))
	for i := range r.endpoints {
		aliases[i] = r.endpoints[i].Alias
	}

	return aliases
}

// Stats returns an immutable per-endpoint health snapshot, safe to call
// concurrently with dispatch. Mutating the returned slice does not affect
// internal state.
//
// Status reflects routing behavior, not just the stored state: a dead endpoint
// whose recover time has elapsed reports [StatusProbation] — selectable again,
// but unconfirmed.
func (r *Router) Stats() []EndpointStat {
	now := r.nowFunc()

	r.mu.Lock()
	active := r.active
	r.mu.Unlock()

	stats := make([]EndpointStat, len(r.endpoints))

	for i := range r.endpoints {
		snap := r.health[i].snapshot()

		state := snap.state
		if state == stateDead && now.Sub(snap.errorTime) >= r.recoverTime {
			state = stateProbation
		}

		stats[i] = EndpointStat{
			Alias:      r.endpoints[i].Alias,
			Status:     string(state),
			Active:     i == active,
			ErrorCount: snap.errorCount,
			LastError:  snap.lastError,
			ErrorTime:  snap.errorTime,
		}
	}

	return stats
}
