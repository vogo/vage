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
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// statusError is a stand-in for a provider's transport error: the router only
// ever reads a status code off it, structurally.
type statusError struct {
	status int
}

func (e *statusError) Error() string   { return fmt.Sprintf("status %d", e.status) }
func (e *statusError) StatusCode() int { return e.status }

// testClock is the router's clock and its retry waiter in one. Waiting records
// the requested duration and advances the clock instead of sleeping, so the
// backoff sequence is asserted exactly and no test pays for it in wall time.
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func (c *testClock) wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)

	return nil
}

func (c *testClock) waited() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.waits...)
}

// attemptLog records which endpoints an attempt closure was invoked for.
type attemptLog struct {
	mu    sync.Mutex
	calls []int
}

func (a *attemptLog) record(idx int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.calls = append(a.calls, idx)
}

func (a *attemptLog) seen() []int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]int(nil), a.calls...)
}

// newClockedRouter builds a router on a controllable clock, with a deterministic
// rng so ordering assertions do not depend on the process seed. Retries are off
// by default — a test that is about retrying says so with WithRetryPolicy.
func newClockedRouter(t *testing.T, strategy Strategy, endpoints []Endpoint, opts ...Option) (*Router, *testClock) {
	t.Helper()

	clock := newTestClock()

	defaults := []Option{WithRetryPolicy(time.Millisecond, 0), WithRecoverTime(time.Hour)}

	r, err := NewRouter(strategy, endpoints, append(defaults, opts...)...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	r.rng = newRand(42)
	r.nowFunc = clock.Now
	r.waitFunc = clock.wait

	return r, clock
}

func newTestRouter(t *testing.T, strategy Strategy, endpoints []Endpoint, opts ...Option) *Router {
	t.Helper()

	r, _ := newClockedRouter(t, strategy, endpoints, opts...)

	return r
}

// endpointsNamed builds simple endpoints with explicit aliases.
func endpointsNamed(aliases ...string) []Endpoint {
	endpoints := make([]Endpoint, len(aliases))
	for i, a := range aliases {
		endpoints[i] = Endpoint{Alias: a}
	}

	return endpoints
}

// dispatchTo runs one dispatch whose attempt succeeds on the endpoints listed
// in ok and fails with the given error elsewhere, returning the endpoint that
// served it (or the dispatch error).
func dispatchTo(ctx context.Context, r *Router, call Call, log *attemptLog, fail error, ok ...int) (int, error) {
	okSet := make(map[int]bool, len(ok))
	for _, idx := range ok {
		okSet[idx] = true
	}

	return Dispatch(ctx, r, call, func(_ context.Context, endpoint int) (int, error) {
		log.record(endpoint)

		if !okSet[endpoint] {
			return 0, fail
		}

		return endpoint, nil
	})
}

func assertIntSlice(t *testing.T, got, want []int) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: got %v", len(got), len(want), got)
	}

	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

// activeAlias returns the alias the pool currently serves from, or "" when it
// has not selected one.
func activeAlias(r *Router) string {
	for _, s := range r.Stats() {
		if s.Active {
			return s.Alias
		}
	}

	return ""
}

func TestNewRouter_EmptyEndpoints(t *testing.T) {
	if _, err := NewRouter(StrategyFailover, nil); err == nil {
		t.Fatal("expected error for empty endpoint list")
	}
}

func TestNewRouter_DerivesAndValidatesAliases(t *testing.T) {
	r, err := NewRouter(StrategyFailover, []Endpoint{{}, {Alias: "named"}, {}})
	if err != nil {
		t.Fatal(err)
	}

	assertStringSlice(t, r.Aliases(), []string{"entry-0", "named", "entry-2"})
}

func TestNewRouter_DuplicateAlias(t *testing.T) {
	_, err := NewRouter(StrategyFailover, endpointsNamed("x", "x"))
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("expected duplicate-alias error, got %v", err)
	}
}

// The router owns its endpoints: deriving aliases may not write back into the
// caller's slice, and later mutations by the caller must not reach routing.
func TestNewRouter_DoesNotMutateCallerEndpoints(t *testing.T) {
	endpoints := []Endpoint{{Weight: 1}, {Weight: 2}}

	r := newTestRouter(t, StrategyFailover, endpoints)

	if endpoints[0].Alias != "" || endpoints[1].Alias != "" {
		t.Fatalf("caller endpoints were written back: %q, %q", endpoints[0].Alias, endpoints[1].Alias)
	}

	endpoints[0].Weight = 99

	if r.endpoints[0].Weight != 1 {
		t.Fatalf("router endpoint follows caller mutation: %d", r.endpoints[0].Weight)
	}
}

// The recovery window has to outlast the backoff scale the retries reach, or a
// "recovered" endpoint would rejoin inside the very interval it failed through.
func TestNewRouter_ValidatesTiming(t *testing.T) {
	cases := []struct {
		name     string
		opts     []Option
		wantText []string
	}{
		{
			name:     "recover time equal to the bound",
			opts:     []Option{WithRetryPolicy(time.Second, 3), WithRecoverTime(8 * time.Second)},
			wantText: []string{"8s", "1s", "2^3"},
		},
		{
			name:     "recover time below the bound",
			opts:     []Option{WithRetryPolicy(time.Second, 3), WithRecoverTime(5 * time.Second)},
			wantText: []string{"5s", "8s"},
		},
		{
			name:     "non-positive base",
			opts:     []Option{WithRetryPolicy(0, 3)},
			wantText: []string{"retry base must be positive"},
		},
		{
			name:     "negative retries",
			opts:     []Option{WithRetryPolicy(time.Second, -1)},
			wantText: []string{"max retries must not be negative"},
		},
		{
			name:     "non-positive recover time",
			opts:     []Option{WithRecoverTime(0)},
			wantText: []string{"recover time must be positive"},
		},
		{
			name:     "backoff overflows a duration",
			opts:     []Option{WithRetryPolicy(time.Hour, 64), WithRecoverTime(time.Hour)},
			wantText: []string{"overflows"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRouter(StrategyFailover, endpointsNamed("a"), tc.opts...)
			if err == nil {
				t.Fatal("expected a construction error")
			}

			for _, want := range tc.wantText {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestNewRouter_AcceptsAValidPolicy(t *testing.T) {
	if _, err := NewRouter(StrategyFailover, endpointsNamed("a"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(8*time.Second+time.Nanosecond)); err != nil {
		t.Fatalf("a recover time just above the bound should be accepted: %v", err)
	}
}

// The heart of the model: one endpoint serves the pool, and keeps serving it.
// Random and weighted are in the table on purpose — they choose *who* becomes
// active, not who serves each call.
func TestDispatch_ActiveEndpointServesEveryCall(t *testing.T) {
	for _, strategy := range []Strategy{StrategyFailover, StrategyRandom, StrategyWeight, StrategyCost, StrategyLatency} {
		t.Run(string(strategy), func(t *testing.T) {
			r := newTestRouter(t, strategy, endpointsNamed("a", "b", "c"))
			log := &attemptLog{}

			first, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1, 2)
			if err != nil {
				t.Fatal(err)
			}

			for range 20 {
				got, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1, 2)
				if err != nil {
					t.Fatal(err)
				}

				if got != first {
					t.Fatalf("call served by %d, want the active endpoint %d", got, first)
				}
			}

			if alias := activeAlias(r); alias != r.endpoints[first].Alias {
				t.Fatalf("Stats reports %q active, want %q", alias, r.endpoints[first].Alias)
			}

			if r.commits != 1 {
				t.Fatalf("committed an active endpoint %d times, want 1", r.commits)
			}
		})
	}
}

// A failing call walks the strategy ordering frozen when selection is needed —
// Random and Weight do not re-draw after each death. Two routers with the same
// seed must agree: the attempt sequence equals that frozen full order.
func TestDispatch_FrozenOrderingWalkedOnFailover(t *testing.T) {
	for _, strategy := range []Strategy{StrategyRandom, StrategyWeight} {
		t.Run(string(strategy), func(t *testing.T) {
			endpoints := []Endpoint{
				{Alias: "a", Weight: 3},
				{Alias: "b", Weight: 1},
				{Alias: "c", Weight: 2},
			}

			peek := newTestRouter(t, strategy, endpoints)
			want := peek.freezeOrdering(Call{}, peek.capableIndices(Call{}))

			r := newTestRouter(t, strategy, endpoints)
			log := &attemptLog{}

			_, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500})

			var multi *MultiError
			if !errors.As(err, &multi) {
				t.Fatalf("expected *MultiError after exhausting the pool, got %v", err)
			}

			assertIntSlice(t, log.seen(), want)

			if len(multi.Errors) != len(want) {
				t.Fatalf("MultiError has %d entries, want %d", len(multi.Errors), len(want))
			}

			for i, idx := range want {
				if multi.Errors[i].Alias != r.endpoints[idx].Alias {
					t.Fatalf("MultiError[%d] alias = %q, want %q", i, multi.Errors[i].Alias, r.endpoints[idx].Alias)
				}
			}
		})
	}
}

// Successful reuse of the active endpoint must not run the strategy. Otherwise
// Random / Weight RNG advances on every happy-path call and later switches
// depend on how many successes happened to precede them.
func TestDispatch_SuccessfulReuseDoesNotAdvanceRNG(t *testing.T) {
	for _, strategy := range []Strategy{StrategyRandom, StrategyWeight} {
		t.Run(string(strategy), func(t *testing.T) {
			endpoints := []Endpoint{
				{Alias: "a", Weight: 3},
				{Alias: "b", Weight: 1},
				{Alias: "c", Weight: 2},
			}

			selectThenReplace := func(reuseCount int) int {
				r := newTestRouter(t, strategy, endpoints)
				log := &attemptLog{}

				first, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1, 2)
				if err != nil {
					t.Fatal(err)
				}

				for range reuseCount {
					got, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1, 2)
					if err != nil {
						t.Fatal(err)
					}

					if got != first {
						t.Fatalf("reuse served by %d, want active %d", got, first)
					}
				}

				// Kill the active endpoint; the replacement is chosen by a fresh
				// freeze of the remaining candidates.
				replacement, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500},
					excludeIndex(first)...)
				if err != nil {
					t.Fatal(err)
				}

				if replacement == first {
					t.Fatal("expected a replacement other than the dead active endpoint")
				}

				return replacement
			}

			if got, want := selectThenReplace(20), selectThenReplace(0); got != want {
				t.Fatalf("replacement after 20 successful reuses = %d, after none = %d — RNG leaked", got, want)
			}
		})
	}
}

func excludeIndex(skip int) []int {
	out := make([]int, 0, 2)

	for i := range 3 {
		if i != skip {
			out = append(out, i)
		}
	}

	return out
}

// A failing active endpoint is retried in place on a doubling schedule, judged
// dead once the retries run out, and replaced without failing the call.
func TestDispatch_RetriesThenReplacesTheActiveEndpoint(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("flaky", "ok"),
		WithRetryPolicy(100*time.Millisecond, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}

	got, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}, 1)
	if err != nil {
		t.Fatal(err)
	}

	if got != 1 {
		t.Fatalf("served by %d, want 1 after the active endpoint died", got)
	}

	// Four attempts on the active endpoint (1 + maxRetries), then one on its
	// replacement — all inside this single call.
	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0, 1})

	wantWaits := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}

	if waits := clock.waited(); !equalDurations(waits, wantWaits) {
		t.Fatalf("retry waits = %v, want %v", waits, wantWaits)
	}

	stats := r.Stats()
	if stats[0].Status != StatusDead || stats[0].Active || stats[0].ErrorCount != 1 {
		t.Fatalf("flaky stats = %+v, want dead/not-active/1", stats[0])
	}

	if stats[1].Status != StatusAvailable || !stats[1].Active {
		t.Fatalf("ok stats = %+v, want available and active", stats[1])
	}

	// One retry round is one entry, not one per attempt.
	if r.commits != 2 {
		t.Fatalf("commits = %d, want 2 (initial selection plus one switch)", r.commits)
	}
}

// 401/403 say the credentials do not work; repeating the call cannot help, so
// the endpoint dies without a single retry or wait.
func TestDispatch_CredentialFailureSkipsRetries(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("expired", "ok"),
				WithRetryPolicy(time.Second, 5), WithRecoverTime(time.Hour))
			log := &attemptLog{}

			got, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: status}, 1)
			if err != nil {
				t.Fatal(err)
			}

			if got != 1 {
				t.Fatalf("served by %d, want 1", got)
			}

			assertIntSlice(t, log.seen(), []int{0, 1})

			if waits := clock.waited(); len(waits) != 0 {
				t.Fatalf("a credential failure must not wait, got %v", waits)
			}

			if s := r.Stats()[0]; s.Status != StatusDead {
				t.Fatalf("expired endpoint status = %q, want dead", s.Status)
			}
		})
	}
}

// Everything that is not a credential failure walks the full retry path first —
// including a 400, which under the old three-state model left health untouched.
func TestDispatch_RetryablesExhaustRetriesBeforeDying(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"400", &statusError{status: 400}},
		{"429", &statusError{status: 429}},
		{"500", &statusError{status: 500}},
		{"transport", errors.New("connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("bad", "ok"),
				WithRetryPolicy(time.Second, 2), WithRecoverTime(time.Hour))
			log := &attemptLog{}

			if _, err := dispatchTo(context.Background(), r, Call{}, log, tc.err, 1); err != nil {
				t.Fatal(err)
			}

			assertIntSlice(t, log.seen(), []int{0, 0, 0, 1})

			wantWaits := []time.Duration{time.Second, 2 * time.Second}
			if waits := clock.waited(); !equalDurations(waits, wantWaits) {
				t.Fatalf("waits = %v, want %v", waits, wantWaits)
			}

			if s := r.Stats()[0]; s.Status != StatusDead || s.LastError == nil {
				t.Fatalf("stats = %+v, want dead with the recorded failure", s)
			}
		})
	}
}

// Recovery restores candidacy and nothing else: the endpoint that took over
// keeps serving, and no switch happens just because an old one came back.
func TestDispatch_RecoveryDoesNotDisplaceTheActiveEndpoint(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("first", "second"),
		WithRecoverTime(time.Minute))
	log := &attemptLog{}

	// "first" dies and "second" takes over.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}, 1); err != nil {
		t.Fatal(err)
	}

	if alias := activeAlias(r); alias != "second" {
		t.Fatalf("active = %q, want second", alias)
	}

	generationAfterSwitch, commitsAfterSwitch := r.generation, r.commits

	// The recovery window elapses: "first" is a candidate again...
	clock.advance(2 * time.Minute)

	if s := r.Stats()[0]; s.Status != StatusProbation || s.Active {
		t.Fatalf("recovered endpoint stats = %+v, want probation and not active", s)
	}

	// ...but the healthy active endpoint keeps every call.
	for range 5 {
		got, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1)
		if err != nil {
			t.Fatal(err)
		}

		if got != 1 {
			t.Fatalf("served by %d, want the incumbent 1", got)
		}
	}

	if r.generation != generationAfterSwitch || r.commits != commitsAfterSwitch {
		t.Fatalf("recovery moved the active endpoint: generation %d→%d, commits %d→%d",
			generationAfterSwitch, r.generation, commitsAfterSwitch, r.commits)
	}
}

// An endpoint back on the clock alone is on probation: the call that picks it up
// spends one attempt on it, not another 1 + maxRetries round with its waits.
// Failing that one attempt restarts the recover window.
func TestDispatch_ProbationSpendsOneAttemptNotARetryRound(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("only"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}
	boom := &statusError{status: 500}

	// The round that judges a confirmed endpoint is the full policy.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); err == nil {
		t.Fatal("expected the endpoint to be judged dead")
	}

	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0})

	if got := len(clock.waited()); got != 3 {
		t.Fatalf("waits in the first round = %d, want 3", got)
	}

	clock.advance(2 * time.Hour)

	if s := r.Stats()[0]; s.Status != StatusProbation {
		t.Fatalf("status after the recover window = %q, want probation", s.Status)
	}

	// The probation attempt costs one request and no wait.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); err == nil {
		t.Fatal("expected the endpoint to be judged dead again")
	}

	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0, 0})

	if got := len(clock.waited()); got != 3 {
		t.Fatalf("waits after the probation attempt = %d, want 3 — probation must not retry", got)
	}

	// And it restarts the recover window rather than leaving the endpoint up.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); !errors.Is(err, ErrNoActiveModels) {
		t.Fatalf("expected ErrNoActiveModels while the new window runs, got %v", err)
	}
}

// One successful call is what ends probation: the endpoint is available outright
// again, failure accounting cleared, and its next failure gets the whole policy.
func TestDispatch_ProbationEndsOnASuccessfulCall(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("only"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}
	boom := &statusError{status: 500}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); err == nil {
		t.Fatal("expected the endpoint to be judged dead")
	}

	clock.advance(2 * time.Hour)

	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0); err != nil {
		t.Fatalf("the probation attempt should have succeeded: %v", err)
	}

	if s := r.Stats()[0]; s.Status != StatusAvailable || s.ErrorCount != 0 || s.LastError != nil {
		t.Fatalf("stats after a confirming call = %+v, want available with cleared accounting", s)
	}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); err == nil {
		t.Fatal("expected the endpoint to be judged dead")
	}

	// 4 + 1 + 4: the confirmed endpoint is retried again like any other.
	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0, 0, 0, 0, 0, 0})
}

// A probation endpoint that is still down does not hold the call up: it answers
// with one attempt and the walk continues to the next candidate at once.
func TestDispatch_ProbationFailureFallsThroughImmediately(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("flaky", "backup", "spare"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}
	boom := &statusError{status: 500}

	// "flaky" dies the slow way; "backup" takes the pool.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom, 1); err != nil {
		t.Fatal(err)
	}

	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0, 1})

	clock.advance(2 * time.Hour)

	// Now "backup" dies too, so the walk reaches "flaky" on probation and then
	// "spare": four attempts for the incumbent, one for the probationer, one that
	// succeeds.
	got, err := dispatchTo(context.Background(), r, Call{}, log, boom, 2)
	if err != nil {
		t.Fatal(err)
	}

	if got != 2 {
		t.Fatalf("served by %d, want spare (2)", got)
	}

	assertIntSlice(t, log.seen(), []int{0, 0, 0, 0, 1, 1, 1, 1, 1, 0, 2})

	if waits := len(clock.waited()); waits != 6 {
		t.Fatalf("waits = %d, want 6 — the probation attempt must add none", waits)
	}
}

// A probation endpoint reached as a temporary pick — because the healthy active
// endpoint cannot serve this call's labels — is still attempted once, and its
// failure leaves the pool's active endpoint alone.
func TestDispatch_ProbationAppliesToATemporaryCapabilityPick(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, []Endpoint{
		{Alias: "generalist", Declares: Declare("basic")},
		{Alias: "specialist", Declares: Declare("basic", "special")},
	}, WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}
	boom := &statusError{status: 500}
	special := Call{Requires: []string{"special"}}

	// The only endpoint that can serve "special" dies the slow way.
	if _, err := dispatchTo(context.Background(), r, special, log, boom); err == nil {
		t.Fatal("expected the specialist to be judged dead")
	}

	assertIntSlice(t, log.seen(), []int{1, 1, 1, 1})

	// A plain call settles the pool on the generalist.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom, 0); err != nil {
		t.Fatal(err)
	}

	clock.advance(2 * time.Hour)

	waitsBefore := len(clock.waited())

	// The active endpoint is healthy but incapable, so the specialist is picked
	// temporarily — on probation, for one attempt.
	if _, err := dispatchTo(context.Background(), r, special, log, boom); err == nil {
		t.Fatal("expected the probation attempt to fail")
	}

	assertIntSlice(t, log.seen(), []int{1, 1, 1, 1, 0, 1})

	if got := len(clock.waited()); got != waitsBefore {
		t.Fatalf("waits = %d, want %d — a temporary probation pick must not retry", got, waitsBefore)
	}

	if alias := activeAlias(r); alias != "generalist" {
		t.Fatalf("active = %q — a temporary pick must not move the pool", alias)
	}
}

// A credential rejection is already retry-free, so probation changes nothing
// about how it is judged: one attempt, dead again, a fresh recover window.
func TestDispatch_ProbationCredentialFailure(t *testing.T) {
	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("only"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))
	log := &attemptLog{}
	rejected := &statusError{status: 401}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, rejected); err == nil {
		t.Fatal("expected a credential failure to kill the endpoint at once")
	}

	clock.advance(2 * time.Hour)

	if _, err := dispatchTo(context.Background(), r, Call{}, log, rejected); err == nil {
		t.Fatal("expected the probation attempt to fail")
	}

	assertIntSlice(t, log.seen(), []int{0, 0})

	if s := r.Stats()[0]; s.Status != StatusDead || s.ErrorCount != 2 {
		t.Fatalf("stats = %+v, want dead with two judgements", s)
	}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, rejected); !errors.Is(err, ErrNoActiveModels) {
		t.Fatalf("expected ErrNoActiveModels while the new window runs, got %v", err)
	}
}

// Nothing left to try is the sentinel; having tried and failed is the aggregate.
func TestDispatch_NoActiveModelsWhenEverythingIsDead(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"))
	log := &attemptLog{}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}); err == nil {
		t.Fatal("expected the first dispatch to fail")
	}

	_, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500})
	if !errors.Is(err, ErrNoActiveModels) {
		t.Fatalf("expected ErrNoActiveModels, got %v", err)
	}

	assertIntSlice(t, log.seen(), []int{0})
}

// Every endpoint failing yields one MultiError entry per endpoint, in the order
// they were tried — a retry round contributes its final error, not four entries.
func TestDispatch_AllFailAttributesEachEndpointOnce(t *testing.T) {
	var seen []AttemptResult

	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b"),
		WithRetryPolicy(time.Millisecond, 1), WithRecoverTime(time.Hour),
		WithAttemptObserver(func(res AttemptResult) { seen = append(seen, res) }))
	log := &attemptLog{}

	_, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500})

	var multi *MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("expected *MultiError, got %T: %v", err, err)
	}

	if len(multi.Errors) != 2 {
		t.Fatalf("endpoint errors = %d, want 2 (one per endpoint)", len(multi.Errors))
	}

	if multi.Errors[0].Alias != "a" || multi.Errors[1].Alias != "b" {
		t.Fatalf("aliases = %q, %q; want a, b", multi.Errors[0].Alias, multi.Errors[1].Alias)
	}

	// Each endpoint was really attempted twice, and the observer saw all four.
	assertIntSlice(t, log.seen(), []int{0, 0, 1, 1})

	if len(seen) != 4 {
		t.Fatalf("observations = %d, want 4 (every attempt including retries)", len(seen))
	}

	// errors.As reaches through the aggregate to a single endpoint failure and
	// on to the backend's own error type.
	var endpointErr *EndpointError
	if !errors.As(err, &endpointErr) {
		t.Fatal("expected errors.As to find an EndpointError in the chain")
	}

	var status *statusError
	if !errors.As(err, &status) || status.StatusCode() != 500 {
		t.Fatal("expected errors.As to reach the backend error through the chain")
	}
}

func TestDispatch_CapabilityFilterFailsFastBeforeAnyAttempt(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "plain", Declares: Declare()},
		{Alias: "also-plain", Declares: Declare("vision")},
	})
	log := &attemptLog{}

	_, err := dispatchTo(context.Background(), r, Call{Requires: []string{"tools"}}, log, nil, 0, 1)

	if !errors.Is(err, ErrCapabilityNotSatisfied) {
		t.Fatalf("expected ErrCapabilityNotSatisfied, got %v", err)
	}

	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *CapabilityError, got %T", err)
	}

	assertStringSlice(t, capErr.Required, []string{"tools"})
	assertStringSlice(t, capErr.Considered, []string{"plain", "also-plain"})

	if len(log.seen()) != 0 {
		t.Fatalf("capability failure must precede every attempt, saw %v", log.seen())
	}
}

// A declared endpoint that satisfies the labels serves the call; an undeclared
// one is unknown rather than incapable and stays in the list.
func TestDispatch_CapabilityFilterKeepsUndeclared(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "no-tools", Declares: Declare()},
		{Alias: "unknown"},
		{Alias: "tools", Declares: Declare("tools")},
	})

	got := r.capableIndices(Call{Requires: []string{"tools"}})
	assertIntSlice(t, got, []int{1, 2})
}

// A call the active endpoint cannot serve is routed around it — for that call
// only. Letting it move the pool would mean one rare capability dragging every
// ordinary call onto a different backend.
func TestDispatch_IncapableActiveEndpointIsRoutedAroundNotReplaced(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "chat-only", Declares: Declare()},
		{Alias: "full", Declares: Declare("special")},
	})
	log := &attemptLog{}

	// An ordinary call makes "chat-only" the pool's active endpoint.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1); err != nil {
		t.Fatal(err)
	}

	if alias := activeAlias(r); alias != "chat-only" {
		t.Fatalf("active = %q, want chat-only", alias)
	}

	commitsBefore := r.commits

	// A call requiring "special" is served by the only endpoint that declares it.
	got, err := dispatchTo(context.Background(), r, Call{Requires: []string{"special"}}, log, nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	if got != 1 {
		t.Fatalf("served by %d, want the capable endpoint 1", got)
	}

	if alias := activeAlias(r); alias != "chat-only" {
		t.Fatalf("active = %q after a temporary pick, want chat-only unchanged", alias)
	}

	if r.commits != commitsBefore {
		t.Fatalf("a temporary pick committed a new active endpoint (%d → %d)", commitsBefore, r.commits)
	}

	// And the ordinary calls are still on the original endpoint.
	back, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	if back != 0 {
		t.Fatalf("ordinary call served by %d, want 0", back)
	}
}

// A temporary pick that fails dies like any other endpoint, but still must not
// disturb the pool's active endpoint.
func TestDispatch_FailedTemporaryPickLeavesTheActiveEndpointAlone(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "chat-only", Declares: Declare()},
		{Alias: "special-a", Declares: Declare("special")},
		{Alias: "special-b", Declares: Declare("special")},
	})
	log := &attemptLog{}

	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0); err != nil {
		t.Fatal(err)
	}

	commitsBefore := r.commits

	// special-a fails, special-b serves — both are temporary picks.
	got, err := dispatchTo(context.Background(), r, Call{Requires: []string{"special"}}, log,
		&statusError{status: 500}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}

	if got != 2 {
		t.Fatalf("served by %d, want 2", got)
	}

	if alias := activeAlias(r); alias != "chat-only" {
		t.Fatalf("active = %q, want chat-only", alias)
	}

	if r.commits != commitsBefore {
		t.Fatalf("a failing temporary pick moved the active endpoint (%d → %d)", commitsBefore, r.commits)
	}

	if s := r.Stats()[1]; s.Status != StatusDead {
		t.Fatalf("special-a status = %q, want dead", s.Status)
	}
}

// Eligible carries a fact about the caller's backend object, not about the
// protocol: endpoints outside it are never attempted.
func TestDispatch_EligibleRestrictsCandidates(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b", "c"))
	log := &attemptLog{}

	got, err := dispatchTo(context.Background(), r, Call{Eligible: []int{1, 2}}, log, errors.New("boom"), 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	if got != 1 {
		t.Fatalf("served by %d, want 1 (endpoint 0 is not eligible)", got)
	}

	assertIntSlice(t, log.seen(), []int{1})
}

func TestDispatch_EligibleCombinesWithCapabilityFilter(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "a", Declares: Declare("tools")},
		{Alias: "b", Declares: Declare()},
		{Alias: "c", Declares: Declare("tools")},
	})

	got := r.capableIndices(Call{Requires: []string{"tools"}, Eligible: []int{1, 2}})
	assertIntSlice(t, got, []int{2})
}

func TestDispatch_CancelledBeforeAttempt(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"))
	log := &attemptLog{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dispatchTo(ctx, r, Call{}, log, nil, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if len(log.seen()) != 0 {
		t.Fatalf("no attempt should be made on a cancelled context, saw %v", log.seen())
	}

	if s := r.Stats()[0]; s.Status != StatusAvailable {
		t.Fatalf("health must not be poisoned by cancellation, got %q", s.Status)
	}
}

// Cancellation mid-attempt is still attributed to its alias, but leaves health
// and the active endpoint untouched: the endpoint did not misbehave.
func TestDispatch_CancelledMidAttemptAttributesButKeepsHealth(t *testing.T) {
	var seen []AttemptResult

	r := newTestRouter(t, StrategyFailover, endpointsNamed("slow", "other"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour),
		WithAttemptObserver(func(res AttemptResult) { seen = append(seen, res) }))

	ctx, cancel := context.WithCancel(context.Background())

	_, err := Dispatch(ctx, r, Call{}, func(_ context.Context, endpoint int) (int, error) {
		cancel() // the attempt is in flight when the caller gives up

		return 0, fmt.Errorf("aborted at %d", endpoint)
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if len(seen) != 1 || seen[0].Alias != "slow" || seen[0].Success {
		t.Fatalf("observations = %+v, want one failed attempt for slow", seen)
	}

	if s := r.Stats()[0]; s.Status != StatusAvailable || s.ErrorCount != 0 {
		t.Fatalf("cancellation changed health: %+v", s)
	}

	if alias := activeAlias(r); alias != "slow" {
		t.Fatalf("active = %q, want slow — cancellation must not switch", alias)
	}
}

// A cancellation arriving during a retry wait ends the call there: no further
// attempt, no health change.
func TestDispatch_CancelledDuringRetryWait(t *testing.T) {
	r, _ := newClockedRouter(t, StrategyFailover, endpointsNamed("a", "b"),
		WithRetryPolicy(time.Second, 3), WithRecoverTime(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())

	// The waiter is where the caller gives up.
	r.waitFunc = func(context.Context, time.Duration) error {
		cancel()

		return ctx.Err()
	}

	log := &attemptLog{}

	_, err := dispatchTo(ctx, r, Call{}, log, &statusError{status: 500}, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	assertIntSlice(t, log.seen(), []int{0})

	if s := r.Stats()[0]; s.Status != StatusAvailable {
		t.Fatalf("an interrupted retry must not judge the endpoint, got %q", s.Status)
	}
}

// Callers racing on a pool whose active endpoint is failing must produce exactly
// one switch between them, not one per caller. Call-slot serialisation rejects
// concurrent dispatches; successive winners reuse the replacement already
// committed, so the commit count stays at two (initial selection plus one switch).
func TestDispatch_ConcurrentFailureSwitchesOnce(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("dying", "healthy"))

	var wg sync.WaitGroup

	for range 40 {
		wg.Go(func() {
			log := &attemptLog{}

			got, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}, 1)
			if errors.Is(err, ErrCallInProgress) {
				return // lost the race for the pool; nothing to assert
			}

			if err != nil {
				t.Errorf("dispatch: %v", err)

				return
			}

			if got != 1 {
				t.Errorf("served by %d, want 1", got)
			}
		})
	}

	wg.Wait()

	// One commit selected "dying", one replaced it with "healthy". Every other
	// caller found the replacement already committed and reused it.
	if r.commits != 2 {
		t.Fatalf("commits = %d, want 2 (initial selection plus a single switch)", r.commits)
	}

	if alias := activeAlias(r); alias != "healthy" {
		t.Fatalf("active = %q, want healthy", alias)
	}
}

// A pool serves one conversation: a second call arriving while one is in flight
// is rejected outright, so the attempt closure never runs for two callers at
// once and nobody queues.
func TestDispatch_RejectsAConcurrentCall(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b"))

	var (
		inFlight atomic.Int32
		peak     atomic.Int32
		served   atomic.Int32
		rejected atomic.Int32
	)

	var wg sync.WaitGroup

	for range 50 {
		wg.Go(func() {
			_, err := Dispatch(context.Background(), r, Call{}, func(context.Context, int) (int, error) {
				n := inFlight.Add(1)
				for {
					high := peak.Load()
					if n <= high || peak.CompareAndSwap(high, n) {
						break
					}
				}

				// Widen the window a real overlap would land in.
				runtime.Gosched()

				inFlight.Add(-1)
				served.Add(1)

				return 0, nil
			})

			switch {
			case err == nil:
			case errors.Is(err, ErrCallInProgress):
				rejected.Add(1)
			default:
				t.Errorf("dispatch: %v", err)
			}
		})
	}

	wg.Wait()

	if peak.Load() != 1 {
		t.Fatalf("peak concurrent attempts = %d, want 1", peak.Load())
	}

	if served.Load()+rejected.Load() != 50 {
		t.Fatalf("served %d + rejected %d, want 50 accounted for", served.Load(), rejected.Load())
	}

	// The slot is free again once the storm passes, so the pool is still usable.
	log := &attemptLog{}
	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1); err != nil {
		t.Fatalf("pool unusable after concurrent rejection: %v", err)
	}
}

// The rejection is immediate and inert: no endpoint is contacted, no health
// changes, and the in-flight call is undisturbed.
func TestDispatch_ConcurrentCallIsRejectedWithoutSideEffects(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b"))

	holding := make(chan struct{})
	finish := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, err := Dispatch(context.Background(), r, Call{}, func(context.Context, int) (int, error) {
			close(holding)
			<-finish

			return 0, nil
		})
		if err != nil {
			t.Errorf("holder dispatch: %v", err)
		}
	})

	<-holding

	before := r.Stats()

	log := &attemptLog{}

	_, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1)
	if !errors.Is(err, ErrCallInProgress) {
		t.Fatalf("expected ErrCallInProgress, got %v", err)
	}

	if len(log.seen()) != 0 {
		t.Fatalf("a rejected call must not attempt anything, saw %v", log.seen())
	}

	after := r.Stats()
	for i := range after {
		if after[i].Status != before[i].Status || after[i].ErrorCount != before[i].ErrorCount {
			t.Fatalf("a rejected call changed health: %+v → %+v", before[i], after[i])
		}
	}

	close(finish)
	wg.Wait()
}

// An unservable call is reported as unservable, not as a busy pool: the
// capability filter runs before the slot is claimed.
func TestDispatch_CapabilityFailureOutranksTheBusyCheck(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "plain", Declares: Declare()},
	})

	holding := make(chan struct{})
	finish := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, err := Dispatch(context.Background(), r, Call{}, func(context.Context, int) (int, error) {
			close(holding)
			<-finish

			return 0, nil
		})
		if err != nil {
			t.Errorf("holder dispatch: %v", err)
		}
	})

	<-holding

	log := &attemptLog{}

	_, err := dispatchTo(context.Background(), r, Call{Requires: []string{"tools"}}, log, nil, 0)
	if !errors.Is(err, ErrCapabilityNotSatisfied) {
		t.Fatalf("expected ErrCapabilityNotSatisfied, got %v", err)
	}

	close(finish)
	wg.Wait()
}

// The slot is released on every exit path, including the failing ones — a router
// that leaked it would deadlock on the next call.
func TestDispatch_ReleasesTheCallSlotOnFailure(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"))
	log := &attemptLog{}

	// Exhausting every endpoint returns a *MultiError.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}); err == nil {
		t.Fatal("expected the dispatch to fail")
	}

	// Nothing left to try returns the sentinel.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0); !errors.Is(err, ErrNoActiveModels) {
		t.Fatalf("expected ErrNoActiveModels, got %v", err)
	}

	// The slot must be free: taking it here would block forever if it leaked.
	select {
	case r.callSlot <- struct{}{}:
		<-r.callSlot
	default:
		t.Fatal("the call slot leaked across a failing dispatch")
	}
}

// Stats reads pool state, not the call slot: it must answer while a call is in
// flight rather than queueing behind it.
func TestStats_NotBlockedByAnInFlightCall(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"))

	holding := make(chan struct{})
	observed := make(chan []EndpointStat, 1)
	finish := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, err := Dispatch(context.Background(), r, Call{}, func(context.Context, int) (int, error) {
			close(holding)
			observed <- r.Stats() // would deadlock if Stats waited for the slot
			<-finish

			return 0, nil
		})
		if err != nil {
			t.Errorf("dispatch: %v", err)
		}
	})

	<-holding

	select {
	case stats := <-observed:
		if len(stats) != 1 {
			t.Fatalf("stats len = %d, want 1", len(stats))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stats blocked behind an in-flight call")
	}

	close(finish)
	wg.Wait()
}

func TestDispatch_ObserverSeesEveryAttempt(t *testing.T) {
	var seen []AttemptResult

	r := newTestRouter(t, StrategyFailover, endpointsNamed("bad", "good"),
		WithAttemptObserver(func(res AttemptResult) { seen = append(seen, res) }))

	log := &attemptLog{}

	if _, err := dispatchTo(context.Background(), r, Call{Stream: true}, log, &statusError{status: 500}, 1); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 2 {
		t.Fatalf("observations = %d, want 2", len(seen))
	}

	if seen[0].Alias != "bad" || seen[0].Success || !seen[0].Stream || seen[0].Err == nil {
		t.Fatalf("first observation = %+v, want bad/failure/stream", seen[0])
	}

	if seen[1].Alias != "good" || !seen[1].Success || !seen[1].Stream || seen[1].Err != nil {
		t.Fatalf("second observation = %+v, want good/success/stream", seen[1])
	}
}

// A nil observer is ignored rather than panicking on the request path.
func TestWithAttemptObserver_NilIgnored(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"), WithAttemptObserver(nil))

	if len(r.attemptObservers) != 0 {
		t.Fatalf("observers = %d, want 0", len(r.attemptObservers))
	}
}

func TestStats_ImmutableSnapshot(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"))

	stats := r.Stats()
	stats[0].Alias = "tampered"
	stats[0].ErrorCount = 999

	if r.endpoints[0].Alias != "a" {
		t.Fatalf("internal alias mutated to %q", r.endpoints[0].Alias)
	}

	if r.Stats()[0].ErrorCount != 0 {
		t.Fatal("internal errorCount mutated via the snapshot")
	}
}

// Before the first call nothing is active: Active is a fact about the pool, not
// a default applied to the first endpoint.
func TestStats_NoActiveBeforeFirstDispatch(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b"))

	for _, s := range r.Stats() {
		if s.Active {
			t.Fatalf("%s reports active before any dispatch", s.Alias)
		}

		if s.Status != StatusAvailable {
			t.Fatalf("%s status = %q, want available", s.Alias, s.Status)
		}
	}
}

func TestStats_ConcurrentWithDispatch(t *testing.T) {
	r := newTestRouter(t, StrategyRandom, endpointsNamed("ok", "flaky"))

	var wg sync.WaitGroup

	for range 30 {
		wg.Go(func() {
			log := &attemptLog{}
			_, _ = dispatchTo(context.Background(), r, Call{}, log, &statusError{status: 500}, 0)
			_ = r.Stats()
		})
	}

	wg.Wait()
}

func TestErrorStrings(t *testing.T) {
	endpointErr := &EndpointError{Alias: "ep", Err: errors.New("boom")}
	if !strings.Contains(endpointErr.Error(), "ep") || !strings.Contains(endpointErr.Error(), "boom") {
		t.Fatalf("EndpointError.Error() = %q", endpointErr.Error())
	}

	multi := &MultiError{Errors: []*EndpointError{
		{Alias: "a", Err: errors.New("x")},
		{Alias: "b", Err: errors.New("y")},
	}}

	if msg := multi.Error(); !strings.Contains(msg, "a: x") || !strings.Contains(msg, "b: y") {
		t.Fatalf("MultiError.Error() = %q", msg)
	}

	if (&MultiError{}).Error() == "" {
		t.Fatal("empty MultiError.Error() should not be empty")
	}

	capErr := &CapabilityError{Required: []string{"vision"}, Considered: []string{"text-only"}}
	if !strings.Contains(capErr.Error(), "vision") || !strings.Contains(capErr.Error(), "text-only") {
		t.Fatalf("CapabilityError.Error() = %q", capErr.Error())
	}
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: got %v", len(got), len(want), got)
	}

	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
