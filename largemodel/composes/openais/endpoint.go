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

package openais

import (
	"fmt"
	"maps"
	"time"

	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/aimodel/openai"
)

// ModelEntry describes a single model backend in the compose client.
type ModelEntry struct {
	// Name is the model identifier sent in the request's Model field — of
	// whichever OpenAI-wire request the call carries. If empty, the request's
	// own model is left in place.
	Name string
	// Client is the underlying API client for this backend. Implementing
	// [Responder] as well as [ChatCompleter] — as *openai.Client and a nested
	// *ComposeClient both do — additionally enrols it in Responses dispatch.
	Client ChatCompleter
	// Weight is used by router.StrategyWeight. Zero is treated as 1.
	Weight int

	// Alias is the endpoint's operational identity, used for health snapshots,
	// and error attribution. It is distinct from Name (the model
	// sent to the backend). When empty on a hand-built entry, a stable alias is
	// derived; explicit aliases must be unique across all entries.
	Alias string

	// Tags carry operational attributes (region/tier/workspace) for observation
	// and future strategies. They never participate in capability decisions.
	Tags map[string]string

	// Capability optionally declares the endpoint's strong-typed capability. A
	// nil value falls back to the client's CapabilityProvider declaration; with
	// neither, the capability is unknown and the endpoint is never excluded by
	// the capability filter.
	Capability *Capability

	// Cost optionally declares static pricing for router.StrategyCost. Nil
	// sorts after priced endpoints.
	Cost *router.EndpointCost

	// Latency optionally declares a routing latency for router.StrategyLatency.
	// Nil sorts after endpoints that carry a latency.
	Latency *time.Duration
}

// EndpointSpec declaratively describes one OpenAI-compatible endpoint: the
// connection coordinates, the model name sent to the backend, and the
// endpoint's operational identity and routing metadata. NewFromEndpoints builds
// an independent openai.Client per spec, so N endpoints no longer require N
// copies of construction code. Those clients implement [Responder] too, so a
// declaratively built pool serves both interaction forms.
type EndpointSpec struct {
	// BaseURL is the endpoint's API base URL.
	BaseURL string
	// APIKey is the endpoint's credential.
	APIKey string
	// Model is the model name sent in the request's Model field for this
	// endpoint.
	Model string
	// Alias is the required, unique operational identity used for health
	// snapshots and error attribution.
	Alias string
	// Weight is used by router.StrategyWeight; Weight <= 0 counts as 1.
	Weight int
	// Tags carry operational attributes (region/tier/workspace). They are
	// copied onto the entry for observation and later strategies; they never
	// participate in capability decisions.
	Tags map[string]string

	// Capability optionally declares the endpoint's strong-typed capability.
	// Leaving it nil keeps the endpoint out of the capability filter entirely
	// (unknown, not incapable).
	Capability *Capability
	// Cost optionally declares static pricing for router.StrategyCost.
	Cost *router.EndpointCost
	// Latency optionally declares a routing latency for router.StrategyLatency.
	Latency *time.Duration
}

// deriveAliases fills in an empty alias from the model Name when that name is
// still free, keeping hand-built entries readable in health snapshots and error
// attribution. Entries it cannot name this way are left empty; the router then
// derives "entry-<index>" and is the single place that rejects duplicates.
// It writes to the slice it is given, which is always the client's own copy —
// never the caller's.
func deriveAliases(entries []ModelEntry) {
	seen := make(map[string]int, len(entries))

	for i := range entries {
		alias := entries[i].Alias

		if alias == "" {
			// Prefer the model name for readable attribution; leave it to the
			// router when the name is empty or already taken (e.g. two endpoints
			// for the same model — the canary case).
			if name := entries[i].Name; name != "" {
				if _, taken := seen[name]; !taken {
					alias = name
					entries[i].Alias = alias
				}
			}
		}

		if alias != "" {
			seen[alias] = i
		}
	}
}

// NewFromEndpoints builds a ComposeClient from declarative endpoint specs. Each
// spec is turned into an independent openai.Client and wrapped in a ModelEntry.
// Advanced callers needing a custom ChatCompleter keep building ModelEntry by
// hand via NewComposeClient.
//
// The native client performs no construction-time validation, so an empty APIKey
// or BaseURL does not fail here; such an endpoint fails at request time.
func NewFromEndpoints(
	strategy router.Strategy, specs []EndpointSpec, opts ...router.Option,
) (*ComposeClient, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("vage/largemodel/composes/openais: at least one endpoint spec is required")
	}

	// Validate aliases up front so the error names the offending position
	// before any client is constructed.
	seen := make(map[string]int, len(specs))

	for i, s := range specs {
		if s.Alias == "" {
			return nil, fmt.Errorf("vage/largemodel/composes/openais: endpoint %d: alias is required", i)
		}

		if prev, dup := seen[s.Alias]; dup {
			return nil, fmt.Errorf("vage/largemodel/composes/openais: duplicate alias %q at endpoints %d and %d", s.Alias, prev, i)
		}

		seen[s.Alias] = i
	}

	entries := make([]ModelEntry, len(specs))

	for i, s := range specs {
		var clientOpts []openai.ClientOption
		if s.BaseURL != "" {
			clientOpts = append(clientOpts, openai.WithBaseURL(s.BaseURL))
		}

		tags := make(map[string]string, len(s.Tags))
		maps.Copy(tags, s.Tags)

		entries[i] = ModelEntry{
			Name:       s.Model,
			Client:     openai.NewClient(s.APIKey, clientOpts...),
			Weight:     s.Weight,
			Alias:      s.Alias,
			Tags:       tags,
			Capability: s.Capability,
			Cost:       s.Cost,
			Latency:    s.Latency,
		}
	}

	return NewComposeClient(strategy, entries, opts...)
}
