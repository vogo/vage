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

package llmagent

import "sync/atomic"

// budgetTracker tracks token consumption against a budget for a single run.
type budgetTracker struct {
	budget   int // 0 means unlimited
	consumed atomic.Int64
}

func newBudgetTracker(budget int) *budgetTracker {
	return &budgetTracker{budget: budget}
}

// Add adds tokens to the consumed total. Returns true if budget is now exhausted.
func (t *budgetTracker) Add(tokens int) bool {
	t.consumed.Add(int64(tokens))
	return t.Exhausted()
}

// Exhausted returns true if consumed >= budget (and budget > 0).
func (t *budgetTracker) Exhausted() bool {
	if t.budget <= 0 {
		return false
	}
	return int(t.consumed.Load()) >= t.budget
}

// Remaining returns budget - consumed (or -1 if unlimited).
func (t *budgetTracker) Remaining() int {
	if t.budget <= 0 {
		return -1
	}
	r := t.budget - int(t.consumed.Load())
	if r < 0 {
		return 0
	}
	return r
}

// Budget returns the configured budget.
func (t *budgetTracker) Budget() int {
	return t.budget
}

// Consumed returns the total tokens consumed so far.
func (t *budgetTracker) Consumed() int {
	return int(t.consumed.Load())
}
