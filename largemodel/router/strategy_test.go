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
	"math"
	"testing"
	"time"
)

// selectAll runs the capability filter plus strategy ordering for a bare call,
// which is the decision the router freezes whenever it starts a dispatch.
func selectAll(r *Router) []int {
	return selectFor(r, Call{})
}

func selectFor(r *Router, call Call) []int {
	return r.freezeOrdering(call, r.capableIndices(call))
}

// markDeadNow takes an endpoint out of rotation for the ordering tests, which
// are about who is *eligible* to become active rather than about dispatch.
func markDeadNow(r *Router, idx int) {
	r.health[idx].markDead(errors.New("fail"), r.nowFunc())
}

func TestSelectFailover_AllActive(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b", "c"))

	assertIntSlice(t, selectAll(r), []int{0, 1, 2})
}

func TestSelectFailover_SkipDead(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b", "c"))
	markDeadNow(r, 1)

	assertIntSlice(t, selectAll(r), []int{0, 2})
}

func TestSelectFailover_AllDead(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a", "b"))
	markDeadNow(r, 0)
	markDeadNow(r, 1)

	if got := selectAll(r); len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestSelectRandom_AllActive(t *testing.T) {
	r := newTestRouter(t, StrategyRandom, endpointsNamed("a", "b", "c"))

	got := selectAll(r)
	if len(got) != 3 {
		t.Fatalf("expected 3 indices, got %d", len(got))
	}

	seen := make(map[int]bool)
	for _, idx := range got {
		seen[idx] = true
	}

	for i := range 3 {
		if !seen[i] {
			t.Fatalf("missing index %d in %v", i, got)
		}
	}
}

func TestSelectRandom_SkipDead(t *testing.T) {
	r := newTestRouter(t, StrategyRandom, endpointsNamed("a", "b", "c"))
	markDeadNow(r, 0)

	got := selectAll(r)
	if len(got) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(got))
	}

	for _, idx := range got {
		if idx == 0 {
			t.Fatal("should not include dead endpoint 0")
		}
	}
}

func TestSelectRandom_Distribution(t *testing.T) {
	r := newTestRouter(t, StrategyRandom, endpointsNamed("a", "b", "c"))

	counts := make(map[int]int)
	iterations := 3000

	for range iterations {
		counts[selectAll(r)[0]]++
	}

	expected := float64(iterations) / 3.0

	for i := range 3 {
		ratio := float64(counts[i]) / expected
		if ratio < 0.7 || ratio > 1.3 {
			t.Fatalf("endpoint %d selected %d times (expected ~%.0f), ratio=%.2f", i, counts[i], expected, ratio)
		}
	}
}

func TestSelectWeighted_ProportionalDistribution(t *testing.T) {
	r := newTestRouter(t, StrategyWeight, []Endpoint{
		{Alias: "heavy", Weight: 3},
		{Alias: "light", Weight: 1},
	})

	counts := make(map[int]int)
	iterations := 4000

	for range iterations {
		counts[selectAll(r)[0]]++
	}

	ratio := float64(counts[0]) / float64(iterations)
	if math.Abs(ratio-0.75) > 0.05 {
		t.Fatalf("heavy selected ratio=%.3f, expected ~0.75", ratio)
	}
}

func TestSelectWeighted_ZeroWeightTreatedAsOne(t *testing.T) {
	r := newTestRouter(t, StrategyWeight, []Endpoint{
		{Alias: "a", Weight: 0},
		{Alias: "b", Weight: 1},
	})

	counts := make(map[int]int)
	iterations := 2000

	for range iterations {
		counts[selectAll(r)[0]]++
	}

	ratio := float64(counts[0]) / float64(iterations)
	if math.Abs(ratio-0.5) > 0.08 {
		t.Fatalf("a selected ratio=%.3f, expected ~0.50", ratio)
	}
}

func TestSelectWeighted_SkipDead(t *testing.T) {
	r := newTestRouter(t, StrategyWeight, []Endpoint{
		{Alias: "a", Weight: 10},
		{Alias: "b", Weight: 1},
	})
	markDeadNow(r, 0)

	got := selectAll(r)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1], got %v", got)
	}
}

// An unknown strategy value falls back to declaration order rather than
// panicking or dropping candidates.
func TestSelectUnknownStrategy_FallsBackToDeclarationOrder(t *testing.T) {
	r := newTestRouter(t, Strategy("no-such-strategy"), endpointsNamed("a", "b"))

	assertIntSlice(t, selectAll(r), []int{0, 1})
}

func TestCostStrategy_DeterministicOrder(t *testing.T) {
	r := newTestRouter(t, StrategyCost, []Endpoint{
		{Alias: "pricey", Cost: &EndpointCost{InputPrice: 5, OutputPrice: 5}},
		{Alias: "cheap", Cost: &EndpointCost{InputPrice: 1, OutputPrice: 1}},
		{Alias: "mid", Cost: &EndpointCost{InputPrice: 3, OutputPrice: 3}},
	})

	// Ascending cost: cheap(2) < mid(6) < pricey(10) → indices 1, 2, 0.
	for range 20 {
		assertIntSlice(t, selectAll(r), []int{1, 2, 0})
	}
}

func TestCostStrategy_MissingDataLast(t *testing.T) {
	r := newTestRouter(t, StrategyCost, []Endpoint{
		{Alias: "unpriced"},
		{Alias: "priced", Cost: &EndpointCost{InputPrice: 9, OutputPrice: 9}},
	})

	assertIntSlice(t, selectAll(r), []int{1, 0})
}

func TestCostStrategy_AliasTieBreak(t *testing.T) {
	r := newTestRouter(t, StrategyCost, []Endpoint{
		{Alias: "zeta", Cost: &EndpointCost{InputPrice: 1, OutputPrice: 1}},
		{Alias: "alpha", Cost: &EndpointCost{InputPrice: 1, OutputPrice: 1}},
	})

	assertIntSlice(t, selectAll(r), []int{1, 0})
}

// OutputUnits is how a wrapper tells the core how much output the call may
// produce, which is what makes cost ordering depend on the request's own cap
// without the core ever seeing the request.
func TestCostStrategy_OutputUnitsFlipTheOrder(t *testing.T) {
	r := newTestRouter(t, StrategyCost, []Endpoint{
		{Alias: "input-heavy", Cost: &EndpointCost{InputPrice: 100, OutputPrice: 0}},
		{Alias: "output-heavy", Cost: &EndpointCost{InputPrice: 0, OutputPrice: 1}},
	})

	// One unit of output: input-heavy=100, output-heavy=1 → output-heavy first.
	assertIntSlice(t, selectFor(r, Call{}), []int{1, 0})
	assertIntSlice(t, selectFor(r, Call{OutputUnits: -5}), []int{1, 0}) // non-positive counts as one

	// A large cap: input-heavy=100, output-heavy=1000 → the order flips.
	assertIntSlice(t, selectFor(r, Call{OutputUnits: 1000}), []int{0, 1})
}

func TestLatencyStrategy_DeterministicOrder(t *testing.T) {
	slow, fast, mid := 50*time.Millisecond, 10*time.Millisecond, 30*time.Millisecond

	r := newTestRouter(t, StrategyLatency, []Endpoint{
		{Alias: "slow", Latency: &slow},
		{Alias: "fast", Latency: &fast},
		{Alias: "mid", Latency: &mid},
	})

	assertIntSlice(t, selectAll(r), []int{1, 2, 0})
}

func TestLatencyStrategy_MissingDataLast(t *testing.T) {
	slow := 50 * time.Millisecond

	r := newTestRouter(t, StrategyLatency, []Endpoint{
		{Alias: "unknown"},
		{Alias: "known", Latency: &slow},
	})

	assertIntSlice(t, selectAll(r), []int{1, 0})
}

func TestLatencyStrategy_AliasTieBreak(t *testing.T) {
	same := 20 * time.Millisecond

	r := newTestRouter(t, StrategyLatency, []Endpoint{
		{Alias: "zeta", Latency: &same},
		{Alias: "alpha", Latency: &same},
	})

	assertIntSlice(t, selectAll(r), []int{1, 0})
}

// The capability filter runs before health: a healthy but incapable endpoint is
// excluded, and a dead capable one is unavailable — so nothing is left.
func TestCapabilityFilter_RunsBeforeHealth(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "healthy-incapable", Declares: Declare()},
		{Alias: "dead-capable", Declares: Declare("tools")},
	})
	markDeadNow(r, 1)

	if got := selectFor(r, Call{Requires: []string{"tools"}}); len(got) != 0 {
		t.Fatalf("expected no available capable candidates, got %v", got)
	}
}

// Declare() with no labels is a declaration of "nothing"; a nil Declares is the
// absence of one. The two must not collapse into each other.
func TestDeclare_EmptyIsNotUndeclared(t *testing.T) {
	if Declare() == nil {
		t.Fatal("Declare() must return a non-nil slice so it reads as a declaration")
	}

	r := newTestRouter(t, StrategyFailover, []Endpoint{
		{Alias: "declares-nothing", Declares: Declare()},
		{Alias: "undeclared"},
	})

	// Required labels exclude the explicit declaration but never the unknown one.
	assertIntSlice(t, selectFor(r, Call{Requires: []string{"tools"}}), []int{1})

	// With nothing required both serve.
	assertIntSlice(t, selectAll(r), []int{0, 1})
}
