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
	"context"
	"fmt"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/provider/openais"
	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/vage/schema"
)

// OpenAIChatComposeCaller is a Caller over one or more OpenAI-compatible
// endpoints. Every OpenAI Chat Completions caller vage builds is one of these:
// a single endpoint is a pool of one, so the reliability story does not change
// shape when a second endpoint is added.
//
// Routing, in-call retries and endpoint health belong to largemodel/router: a
// pool retries its active endpoint with exponential waits (500ms doubling,
// three retries by default), marks it dead when they are exhausted, and fails
// over to the next candidate the strategy names — or, with nothing to fail
// over to, returns router.ErrNoActiveModels until the recover window (60s by
// default) elapses. [WithComposeRouterOptions] tunes all three.
//
// A recovered endpoint comes back on probation rather than restored: the clock
// proves nothing on its own, so the next real call re-tests it with a single
// attempt instead of a whole retry round, and only a success promotes it. That
// is what Stats reports as router.StatusProbation.
//
// One consequence deserves stating plainly: the router judges only HTTP 401 and
// 403 as unretryable, so a deterministic 400 is retried like a transient
// failure and then costs the endpoint its recover window. [IsRetryable] is
// vage's own, narrower reading of the same error, for callers deciding whether
// a failure is worth reacting to.
type OpenAIChatComposeCaller struct {
	caller   Caller
	pool     *composePool[*openais.ComposeClient]
	declared []EndpointCapability
}

// Protocol implements Caller.
func (c *OpenAIChatComposeCaller) Protocol() schema.Protocol { return c.caller.Protocol() }

// Call implements Caller.
func (c *OpenAIChatComposeCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	return c.caller.Call(ctx, req)
}

// CallStream implements Caller.
func (c *OpenAIChatComposeCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return c.caller.CallStream(ctx, req)
}

// newOpenAIComposeCaller wires a pool set built by build behind the OpenAI
// caller. It is what both entry points — one endpoint and several — end up
// calling, so the two differ only in how a pool is built.
func newOpenAIComposeCaller(
	build func() (*openais.ComposeClient, error), cfg *composeConfig, declared []EndpointCapability,
) (*OpenAIChatComposeCaller, error) {
	pool, err := newComposePool(cfg.concurrency, build)
	if err != nil {
		return nil, err
	}

	return &OpenAIChatComposeCaller{
		caller:   newOpenAIChatCaller(&openAIComposeBackend{pool: pool}),
		pool:     pool,
		declared: declared,
	}, nil
}

// Stats reports endpoint health merged across the caller's pools. See
// mergeEndpointStats for what merging means when pools disagree.
//
// EndpointStats is an alias for Stats.
func (c *OpenAIChatComposeCaller) Stats() []router.EndpointStat {
	return mergeEndpointStats(c.pool.snapshot(func(cc *openais.ComposeClient) []router.EndpointStat {
		return cc.Stats()
	}))
}

// EndpointStats reports endpoint health merged across the caller's pools.
func (c *OpenAIChatComposeCaller) EndpointStats() []router.EndpointStat { return c.Stats() }

func (c *OpenAIChatComposeCaller) Capabilities(_ context.Context, req *Request) (Capabilities, error) {
	return resolveDeclaredCapabilities(c.declared, req), nil
}

func (c *OpenAIChatComposeCaller) EndpointCapabilities() []EndpointCapability {
	out := make([]EndpointCapability, len(c.declared))
	copy(out, c.declared)

	return out
}

// openAIComposeBackend borrows a pool for the duration of one call.
type openAIComposeBackend struct {
	pool *composePool[*openais.ComposeClient]
}

// ChatCompletions implements OpenAIChatBackend.
func (b *openAIComposeBackend) ChatCompletions(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionResponse, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.ChatCompletions(ctx, request)
}

// ChatCompletionsStream implements OpenAIChatBackend. The pool is released as
// soon as the stream is established: routing covers establishment
// only, and the stream that follows no longer passes through it.
func (b *openAIComposeBackend) ChatCompletionsStream(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionStream, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.ChatCompletionsStream(ctx, request)
}

// openAIComposeOptions carries the OpenAI client options a caller supplied for
// the clients this file builds. It lives with the rest of the OpenAI glue
// rather than in the neutral compose config, so the vendor option type stays
// on this side of the provider boundary.
type openAIComposeOptions struct {
	clientOpts []openai.ClientOption
}

// WithOpenAIClientOptions passes provider client options — base URL overrides,
// timeouts, custom headers — to the clients [BuildCaller] builds for an
// [OpenAIConfig]. It has no effect on a pool built from endpoint specs, which carries
// its connection details per endpoint.
func WithOpenAIClientOptions(opts ...openai.ClientOption) ComposeOption {
	return func(c *composeConfig) {
		c.openAI.clientOpts = append(c.openAI.clientOpts, opts...)
	}
}

// newOpenAIChatCallerFromConfig builds a caller over one or more
// OpenAI-compatible endpoints described by cfg. It is the single construction
// path for this protocol: [BuildCaller] hands it a config, [NewCaller] hands
// it a one-element one.
func newOpenAIChatCallerFromConfig(cfg OpenAIConfig, opts ...CallerOption) (*OpenAIChatComposeCaller, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("vage: at least one OpenAI endpoint is required")
	}

	composeCfg := newComposeConfig(opts...)

	declared, err := declaredFromOpenAI(cfg.Endpoints)
	if err != nil {
		return nil, err
	}

	return newOpenAIComposeCaller(func() (*openais.ComposeClient, error) {
		return buildOpenAIComposeClient(cfg, composeCfg)
	}, composeCfg, declared)
}

// buildOpenAIComposeClient turns the neutral endpoint configuration into a
// provider pool. The declarative path lets the provider construct its own
// clients; only a caller-supplied client option forces construction here,
// because those options have no place in an endpoint spec.
func buildOpenAIComposeClient(cfg OpenAIConfig, composeCfg *composeConfig) (*openais.ComposeClient, error) {
	strategy := strategyOrFailover(cfg.Strategy)

	if len(composeCfg.openAI.clientOpts) == 0 {
		return openais.NewFromEndpoints(strategy, toOpenAISpecs(cfg.Endpoints), composeCfg.routerOpts...)
	}

	entries := make([]openais.ModelEntry, len(cfg.Endpoints))

	for i, e := range cfg.Endpoints {
		clientOpts := composeCfg.openAI.clientOpts
		if e.BaseURL != "" {
			clientOpts = append([]openai.ClientOption{openai.WithBaseURL(e.BaseURL)}, clientOpts...)
		}

		entries[i] = openais.ModelEntry{
			Name:       e.Model,
			Client:     openai.NewClient(e.APIKey, clientOpts...),
			Weight:     e.Weight,
			Alias:      e.Alias,
			Tags:       e.Tags,
			Capability: openAIProviderCapability(e.Capabilities),
			Cost:       e.Cost,
			Latency:    e.Latency,
		}
	}

	return openais.NewComposeClient(strategy, entries, composeCfg.routerOpts...)
}

// toOpenAISpecs maps the neutral endpoint configuration onto the provider's
// own endpoint specs.
func toOpenAISpecs(endpoints []OpenAIEndpoint) []openais.EndpointSpec {
	specs := make([]openais.EndpointSpec, len(endpoints))

	for i, e := range endpoints {
		specs[i] = openais.EndpointSpec{
			BaseURL:    e.BaseURL,
			APIKey:     e.APIKey,
			Model:      e.Model,
			Alias:      e.Alias,
			Weight:     e.Weight,
			Tags:       e.Tags,
			Capability: openAIProviderCapability(e.Capabilities),
			Cost:       e.Cost,
			Latency:    e.Latency,
		}
	}

	return specs
}

var (
	_ Caller                     = (*OpenAIChatComposeCaller)(nil)
	_ CapabilityProvider         = (*OpenAIChatComposeCaller)(nil)
	_ EndpointCapabilityProvider = (*OpenAIChatComposeCaller)(nil)
	_ OpenAIChatBackend          = (*openAIComposeBackend)(nil)
)
