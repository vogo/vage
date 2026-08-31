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
	"time"

	"github.com/vogo/vage/largemodel/router"
)

// Routing types re-exported from the internal router so callers need not import
// github.com/vogo/vage/largemodel/router directly.
type (
	Strategy      = router.Strategy
	EndpointStat  = router.EndpointStat
	AttemptResult = router.AttemptResult
	EndpointCost  = router.EndpointCost
)

const (
	StrategyFailover = router.StrategyFailover
	StrategyRandom   = router.StrategyRandom
	StrategyWeight   = router.StrategyWeight
	StrategyCost     = router.StrategyCost
	StrategyLatency  = router.StrategyLatency

	StatusAvailable = router.StatusAvailable
	StatusDead      = router.StatusDead
	StatusProbation = router.StatusProbation
)

// ErrNoActiveEndpoints reports that every endpoint capable of serving the call
// was dead and none had recovered yet.
var ErrNoActiveEndpoints = router.ErrNoActiveModels

// CallerOption configures a routed model caller (retry policy, concurrency,
// provider client options). ComposeOption is a deprecated alias.
type CallerOption = ComposeOption

// OpenAIEndpoint describes one OpenAI-compatible backend in a routed caller.
type OpenAIEndpoint struct {
	Alias   string
	APIKey  string
	BaseURL string
	Model   string
	Weight  int
	Tags    map[string]string
	Cost    *EndpointCost
	Latency *time.Duration
}

// OpenAIConfig holds the endpoints and selection strategy for an OpenAI Chat
// Completions caller. A single endpoint is a pool of one — the same reliability
// behaviour applies whether there is one backend or several.
type OpenAIConfig struct {
	Endpoints []OpenAIEndpoint
	Strategy  Strategy
}

// AnthropicEndpoint describes one Anthropic Messages backend in a routed caller.
type AnthropicEndpoint struct {
	Alias   string
	APIKey  string
	BaseURL string
	Model   string
	Weight  int
	Tags    map[string]string
	Version string
	Beta    []string
	Cost    *EndpointCost
	Latency *time.Duration
}

// AnthropicConfig holds the endpoints and selection strategy for an Anthropic
// Messages caller.
type AnthropicConfig struct {
	Endpoints []AnthropicEndpoint
	Strategy  Strategy
}

func strategyOrFailover(s Strategy) Strategy {
	if s == "" {
		return StrategyFailover
	}

	return s
}
