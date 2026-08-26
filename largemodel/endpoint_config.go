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
	"fmt"
	"time"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/provider/anthropics"
	"github.com/vogo/vage/largemodel/provider/openais"
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

func toOpenAISpecs(endpoints []OpenAIEndpoint) []openais.EndpointSpec {
	specs := make([]openais.EndpointSpec, len(endpoints))

	for i, e := range endpoints {
		specs[i] = openais.EndpointSpec{
			BaseURL: e.BaseURL,
			APIKey:  e.APIKey,
			Model:   e.Model,
			Alias:   e.Alias,
			Weight:  e.Weight,
			Tags:    e.Tags,
			Cost:    e.Cost,
			Latency: e.Latency,
		}
	}

	return specs
}

func toAnthropicSpecs(endpoints []AnthropicEndpoint) []anthropics.EndpointSpec {
	specs := make([]anthropics.EndpointSpec, len(endpoints))

	for i, e := range endpoints {
		specs[i] = anthropics.EndpointSpec{
			BaseURL: e.BaseURL,
			APIKey:  e.APIKey,
			Model:   e.Model,
			Alias:   e.Alias,
			Weight:  e.Weight,
			Tags:    e.Tags,
			Version: e.Version,
			Beta:    e.Beta,
			Cost:    e.Cost,
			Latency: e.Latency,
		}
	}

	return specs
}

// NewOpenAIChatCallerFromConfig builds a Caller over one or more
// OpenAI-compatible endpoints described by cfg.
func NewOpenAIChatCallerFromConfig(cfg OpenAIConfig, opts ...CallerOption) (*OpenAIChatComposeCaller, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("vage: at least one OpenAI endpoint is required")
	}

	composeCfg := newComposeConfig(opts...)

	return newOpenAIComposeCaller(func() (*openais.ComposeClient, error) {
		return buildOpenAIComposeClient(cfg, composeCfg)
	}, composeCfg)
}

func buildOpenAIComposeClient(cfg OpenAIConfig, composeCfg *composeConfig) (*openais.ComposeClient, error) {
	strategy := strategyOrFailover(cfg.Strategy)

	if len(composeCfg.openAIClientOpts) == 0 {
		return openais.NewFromEndpoints(strategy, toOpenAISpecs(cfg.Endpoints), composeCfg.routerOpts...)
	}

	entries := make([]openais.ModelEntry, len(cfg.Endpoints))
	for i, e := range cfg.Endpoints {
		clientOpts := composeCfg.openAIClientOpts
		if e.BaseURL != "" {
			clientOpts = append([]openai.ClientOption{openai.WithBaseURL(e.BaseURL)}, clientOpts...)
		}

		entries[i] = openais.ModelEntry{
			Name:    e.Model,
			Client:  openai.NewClient(e.APIKey, clientOpts...),
			Weight:  e.Weight,
			Alias:   e.Alias,
			Tags:    e.Tags,
			Cost:    e.Cost,
			Latency: e.Latency,
		}
	}

	return openais.NewComposeClient(strategy, entries, composeCfg.routerOpts...)
}

// NewAnthropicMessagesCallerFromConfig builds a Caller over one or more
// Anthropic-compatible endpoints described by cfg.
func NewAnthropicMessagesCallerFromConfig(cfg AnthropicConfig, opts ...CallerOption) (*AnthropicMessagesComposeCaller, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("vage: at least one Anthropic endpoint is required")
	}

	composeCfg := newComposeConfig(opts...)

	return newAnthropicComposeCaller(func() (*anthropics.ComposeClient, error) {
		return buildAnthropicComposeClient(cfg, composeCfg)
	}, composeCfg)
}

func buildAnthropicComposeClient(cfg AnthropicConfig, composeCfg *composeConfig) (*anthropics.ComposeClient, error) {
	strategy := strategyOrFailover(cfg.Strategy)

	if len(composeCfg.anthropicClientOpts) == 0 {
		return anthropics.NewFromEndpoints(strategy, toAnthropicSpecs(cfg.Endpoints), composeCfg.routerOpts...)
	}

	entries := make([]anthropics.ModelEntry, len(cfg.Endpoints))
	for i, e := range cfg.Endpoints {
		clientOpts := composeCfg.anthropicClientOpts
		if e.BaseURL != "" {
			clientOpts = append([]anthropic.ClientOption{anthropic.WithBaseURL(e.BaseURL)}, clientOpts...)
		}

		if e.Version != "" {
			clientOpts = append(clientOpts, anthropic.WithVersion(e.Version))
		}

		if len(e.Beta) > 0 {
			clientOpts = append(clientOpts, anthropic.WithBeta(e.Beta...))
		}

		entries[i] = anthropics.ModelEntry{
			Name:    e.Model,
			Client:  anthropic.NewClient(e.APIKey, clientOpts...),
			Weight:  e.Weight,
			Alias:   e.Alias,
			Tags:    e.Tags,
			Cost:    e.Cost,
			Latency: e.Latency,
		}
	}

	return anthropics.NewComposeClient(strategy, entries, composeCfg.routerOpts...)
}
