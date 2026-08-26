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
	"sync"
	"testing"
	"time"
)

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want failureOutcome
	}{
		{"401 is a credential failure", &statusError{status: 401}, outcomeCredential},
		{"403 is a credential failure", &statusError{status: 403}, outcomeCredential},
		{"429 retries", &statusError{status: 429}, outcomeRetryable},
		{"400 retries", &statusError{status: 400}, outcomeRetryable},
		{"404 retries", &statusError{status: 404}, outcomeRetryable},
		{"500 retries", &statusError{status: 500}, outcomeRetryable},
		{"503 retries", &statusError{status: 503}, outcomeRetryable},
		{"status 0 retries", &statusError{status: 0}, outcomeRetryable},
		{"transport failures retry", errors.New("connection refused"), outcomeRetryable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFailure(tc.err); got != tc.want {
				t.Fatalf("classifyFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEndpointHealth_NewIsAvailable(t *testing.T) {
	if !newEndpointHealth().available(time.Now(), time.Minute) {
		t.Fatal("a new endpointHealth should be available")
	}
}

func TestEndpointHealth_DeadUntilRecoverElapses(t *testing.T) {
	h := newEndpointHealth()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recover := time.Minute

	h.markDead(errors.New("fail"), now)

	if h.available(now, recover) {
		t.Fatal("a dead endpoint must not be selectable immediately")
	}

	if h.available(now.Add(59*time.Second), recover) {
		t.Fatal("a dead endpoint must stay out of rotation before recover elapses")
	}

	if !h.available(now.Add(recover), recover) {
		t.Fatal("a dead endpoint should be selectable at the recover boundary")
	}

	if !h.available(now.Add(2*recover), recover) {
		t.Fatal("a dead endpoint should stay selectable after recover")
	}
}

// Recovery is purely a clock fact: the stored state remains dead, so the
// endpoint's last error stays visible for attribution.
func TestEndpointHealth_RecoveryKeepsTheRecordedFailure(t *testing.T) {
	h := newEndpointHealth()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	h.markDead(errors.New("gone"), now)

	if !h.available(now.Add(time.Hour), time.Minute) {
		t.Fatal("expected recovery")
	}

	snap := h.snapshot()
	if snap.state != stateDead || snap.lastError == nil || snap.errorCount != 1 {
		t.Fatalf("snapshot after recovery = %+v; the failure record must survive", snap)
	}
}

// Clock recovery restores candidacy provisionally: the endpoint is selectable,
// and flagged so the attempt that picks it up does not spend a retry round on
// what the clock cannot prove. Only a success ends that.
func TestEndpointHealth_ClockRecoveryIsProvisional(t *testing.T) {
	h := newEndpointHealth()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recover := time.Minute

	if ok, probation := h.selectable(now, recover); !ok || probation {
		t.Fatalf("a fresh endpoint = (%v, %v), want selectable outright", ok, probation)
	}

	h.markDead(errors.New("fail"), now)

	if ok, probation := h.selectable(now, recover); ok || probation {
		t.Fatalf("a dead endpoint = (%v, %v), want unselectable", ok, probation)
	}

	if ok, probation := h.selectable(now.Add(recover), recover); !ok || !probation {
		t.Fatalf("a clock-recovered endpoint = (%v, %v), want selectable on probation", ok, probation)
	}

	h.markSuccess()

	if ok, probation := h.selectable(now.Add(recover), recover); !ok || probation {
		t.Fatalf("a confirmed endpoint = (%v, %v), want selectable outright", ok, probation)
	}
}

func TestEndpointHealth_SuccessResetsAccounting(t *testing.T) {
	h := newEndpointHealth()
	now := time.Now()

	h.markDead(errors.New("e1"), now)
	h.markDead(errors.New("e2"), now.Add(time.Second))

	if snap := h.snapshot(); snap.errorCount != 2 {
		t.Fatalf("error count = %d, want 2", snap.errorCount)
	}

	h.markSuccess()

	snap := h.snapshot()
	if snap.state != stateAvailable || snap.errorCount != 0 || snap.lastError != nil || !snap.errorTime.IsZero() {
		t.Fatalf("after success: state=%s count=%d err=%v time=%v",
			snap.state, snap.errorCount, snap.lastError, snap.errorTime)
	}
}

func TestEndpointHealth_ConcurrentAccess(t *testing.T) {
	h := newEndpointHealth()
	now := time.Now()

	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			if n%2 == 0 {
				h.markDead(errors.New("fail"), now)
			} else {
				h.markSuccess()
			}

			h.available(now, time.Minute)
			h.snapshot()
		}(i)
	}

	wg.Wait()
}
