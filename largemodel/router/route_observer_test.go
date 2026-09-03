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
	"sync"
	"testing"
	"time"
)

func TestDispatch_RouteObserverReasons(t *testing.T) {
	var mu sync.Mutex
	var seen []RouteSelection
	obs := WithRouteObserver(func(_ context.Context, sel RouteSelection) {
		mu.Lock()
		seen = append(seen, sel)
		mu.Unlock()
	})

	r, clock := newClockedRouter(t, StrategyFailover, endpointsNamed("primary", "backup"),
		WithRetryPolicy(time.Millisecond, 0), WithRecoverTime(time.Minute), obs)
	log := &attemptLog{}
	boom := &statusError{status: 500}

	// First call: primary dies, backup serves — initial then failover.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom, 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]RouteSelection(nil), seen...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("selections = %d, want at least 2 (initial + failover)", len(got))
	}
	if got[0].Reason != RouteReasonInitial || got[0].Alias != "primary" {
		t.Errorf("first = %+v, want alias=primary reason=initial", got[0])
	}
	if got[1].Reason != RouteReasonFailover || got[1].Alias != "backup" {
		t.Errorf("second = %+v, want alias=backup reason=failover", got[1])
	}
	if got[0].Strategy != StrategyFailover {
		t.Errorf("strategy = %q, want failover", got[0].Strategy)
	}

	// Second call reuses the healthy backup.
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got = append([]RouteSelection(nil), seen...)
	mu.Unlock()
	if len(got) != 1 || got[0].Reason != RouteReasonReuse || got[0].Alias != "backup" {
		t.Errorf("reuse = %+v, want alias=backup reason=reuse", got)
	}

	// Kill backup, wait for recover, pick primary on probation.
	if _, err := dispatchTo(context.Background(), r, Call{}, log, boom); err == nil {
		t.Fatal("expected backup to die")
	}
	clock.advance(2 * time.Minute)
	mu.Lock()
	seen = nil
	mu.Unlock()
	if _, err := dispatchTo(context.Background(), r, Call{}, log, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got = append([]RouteSelection(nil), seen...)
	mu.Unlock()
	foundProbation := false
	for _, sel := range got {
		if sel.Reason == RouteReasonProbation {
			foundProbation = true
		}
		if sel.Alias == "" || string(sel.Strategy) == "" {
			t.Errorf("selection missing identity: %+v", sel)
		}
	}
	if !foundProbation {
		t.Errorf("selections after recover = %+v, want a probation reason", got)
	}
}

func TestWithRouteObserver_NilIgnored(t *testing.T) {
	r := newTestRouter(t, StrategyFailover, endpointsNamed("a"), WithRouteObserver(nil))
	if _, err := Dispatch(context.Background(), r, Call{}, func(context.Context, int) (int, error) {
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
}
