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
	"math/rand"
	"slices"
)

// Strategy decides which endpoint becomes the pool's active one. It runs when
// the router has no active endpoint or is replacing a dead one — not on every
// call. A strategy therefore expresses a *preference between backends*, not a
// distribution over requests: with the active model, consecutive successful
// calls all land on the same endpoint whichever strategy is configured.
//
// Each strategy still produces a full ordering rather than a single pick.
// [Dispatch] freezes that ordering once per call, so a failing call walks the
// same sequence as endpoints die under it rather than re-drawing after each
// failure (see freezeOrdering).
type Strategy string

const (
	// StrategyFailover prefers endpoints in declaration order: the first
	// available one becomes active. It is the default.
	StrategyFailover Strategy = "failover"
	// StrategyRandom picks a uniformly random available endpoint.
	StrategyRandom Strategy = "random"
	// StrategyWeight picks an available endpoint with probability proportional to
	// weight; Weight <= 0 counts as 1.
	StrategyWeight Strategy = "weighted"
	// StrategyCost prefers the endpoint with the lowest static cost (EndpointCost).
	StrategyCost Strategy = "cost"
	// StrategyLatency prefers the endpoint with the lowest injected latency.
	StrategyLatency Strategy = "latency"
)

// freezeOrdering returns the strategy's full ordering over the endpoints that
// may serve this call: the capability-filtered indices, narrowed to those
// selectable right now — which includes endpoints on probation, whose candidacy
// a strategy treats no differently from a confirmed one. [Dispatch] calls it the
// first time a pick cannot reuse the active endpoint, so failover walks one
// frozen sequence and successful active reuse never advances Random / Weight RNG.
//
// The caller must not hold r.mu.
func (r *Router) freezeOrdering(call Call, capable []int) []int {
	now := r.nowFunc()

	r.mu.Lock()
	defer r.mu.Unlock()

	available := make([]int, 0, len(capable))

	for _, idx := range capable {
		if r.health[idx].available(now, r.recoverTime) {
			available = append(available, idx)
		}
	}

	return r.orderByStrategy(call, available)
}

// orderByStrategy orders an already-available candidate slice per the router's
// strategy. The input slice is in declaration order and is not mutated.
//
// The caller must hold r.mu: the random and weighted orderings draw from the
// router's rng, and every caller reaches this while deciding the active
// endpoint, which is protected by the same lock.
func (r *Router) orderByStrategy(call Call, available []int) []int {
	switch r.strategy {
	case StrategyRandom:
		return r.orderRandom(available)
	case StrategyWeight:
		return r.orderWeighted(available)
	case StrategyCost:
		return r.sortByCost(slices.Clone(available), call)
	case StrategyLatency:
		return r.sortByLatency(slices.Clone(available))
	default: // StrategyFailover and any unknown value.
		return slices.Clone(available)
	}
}

// orderRandom returns a shuffled copy of the available indices. The caller holds
// r.mu, which is what protects the rng.
func (r *Router) orderRandom(available []int) []int {
	result := slices.Clone(available)

	r.rng.Shuffle(len(result), func(i, j int) {
		result[i], result[j] = result[j], result[i]
	})

	return result
}

// orderWeighted orders the available indices by sampling without replacement in
// proportion to weight; Weight <= 0 counts as 1. The caller holds r.mu.
func (r *Router) orderWeighted(available []int) []int {
	type candidate struct {
		idx    int
		weight int
	}

	candidates := make([]candidate, 0, len(available))

	for _, idx := range available {
		w := r.endpoints[idx].Weight
		if w <= 0 {
			w = 1
		}

		candidates = append(candidates, candidate{idx: idx, weight: w})
	}

	result := make([]int, 0, len(candidates))

	for len(candidates) > 0 {
		total := 0
		for _, cand := range candidates {
			total += cand.weight
		}

		n := r.rng.Intn(total)
		cumulative := 0

		for j, cand := range candidates {
			cumulative += cand.weight

			if n < cumulative {
				result = append(result, cand.idx)
				candidates = append(candidates[:j], candidates[j+1:]...)

				break
			}
		}
	}

	return result
}

// newRand returns a deterministic rand for testing or a seeded real rand.
func newRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}
