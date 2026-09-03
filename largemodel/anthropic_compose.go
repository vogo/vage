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

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/provider/anthropics"
	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/vage/schema"
)

// AnthropicMessagesComposeCaller is a Caller over one or more
// Anthropic-compatible endpoints, and like its OpenAI counterpart it is what
// every Anthropic Messages caller vage builds actually is. The division of
// labour is the same: routing, retries and endpoint health belong to
// largemodel/router, including the reading of a 400 as retryable.
type AnthropicMessagesComposeCaller struct {
	caller   Caller
	pool     *composePool[*anthropics.ComposeClient]
	declared []EndpointCapability
}

// Protocol implements Caller.
func (c *AnthropicMessagesComposeCaller) Protocol() schema.Protocol { return c.caller.Protocol() }

// Call implements Caller.
func (c *AnthropicMessagesComposeCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	return c.caller.Call(ctx, req)
}

// CallStream implements Caller.
func (c *AnthropicMessagesComposeCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return c.caller.CallStream(ctx, req)
}

// newAnthropicComposeCaller wires a pool set built by build behind the
// Anthropic caller, for both the one-endpoint and the several-endpoint path.
func newAnthropicComposeCaller(
	build func() (*anthropics.ComposeClient, error), cfg *composeConfig, declared []EndpointCapability,
) (*AnthropicMessagesComposeCaller, error) {
	pool, err := newComposePool(cfg.concurrency, build)
	if err != nil {
		return nil, err
	}

	return &AnthropicMessagesComposeCaller{
		caller:   newAnthropicMessagesCaller(&anthropicComposeBackend{pool: pool}),
		pool:     pool,
		declared: declared,
	}, nil
}

// Stats reports endpoint health merged across the caller's pools.
func (c *AnthropicMessagesComposeCaller) Stats() []router.EndpointStat {
	return mergeEndpointStats(c.pool.snapshot(func(cc *anthropics.ComposeClient) []router.EndpointStat {
		return cc.Stats()
	}))
}

// EndpointStats reports endpoint health merged across the caller's pools.
func (c *AnthropicMessagesComposeCaller) EndpointStats() []router.EndpointStat { return c.Stats() }

func (c *AnthropicMessagesComposeCaller) Capabilities(_ context.Context, req *Request) (Capabilities, error) {
	return resolveDeclaredCapabilities(c.declared, req), nil
}

func (c *AnthropicMessagesComposeCaller) EndpointCapabilities() []EndpointCapability {
	out := make([]EndpointCapability, len(c.declared))
	copy(out, c.declared)

	return out
}

// anthropicComposeBackend borrows a pool for the duration of one call.
type anthropicComposeBackend struct {
	pool *composePool[*anthropics.ComposeClient]
}

// Messages implements AnthropicMessagesBackend.
func (b *anthropicComposeBackend) Messages(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessagesResponse, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.Messages(ctx, request)
}

// MessagesStream implements AnthropicMessagesBackend. The pool is released as
// soon as the stream is established.
func (b *anthropicComposeBackend) MessagesStream(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessageStream, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.MessagesStream(ctx, request)
}

// anthropicComposeOptions carries the Anthropic client options a caller
// supplied for the clients this file builds, keeping the vendor option type on
// this side of the provider boundary.
type anthropicComposeOptions struct {
	clientOpts []anthropic.ClientOption
}

// WithAnthropicClientOptions is the Anthropic counterpart of
// [WithOpenAIClientOptions], for the clients [BuildCaller] builds for an
// [AnthropicConfig].
func WithAnthropicClientOptions(opts ...anthropic.ClientOption) ComposeOption {
	return func(c *composeConfig) {
		c.anthropic.clientOpts = append(c.anthropic.clientOpts, opts...)
	}
}

// newAnthropicMessagesCallerFromConfig builds a caller over one or more
// Anthropic-compatible endpoints described by cfg. As on the OpenAI side it is
// the single construction path this protocol has.
func newAnthropicMessagesCallerFromConfig(
	cfg AnthropicConfig, opts ...CallerOption,
) (*AnthropicMessagesComposeCaller, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("vage: at least one Anthropic endpoint is required")
	}

	composeCfg := newComposeConfig(opts...)

	declared, err := declaredFromAnthropic(cfg.Endpoints)
	if err != nil {
		return nil, err
	}

	return newAnthropicComposeCaller(func() (*anthropics.ComposeClient, error) {
		return buildAnthropicComposeClient(cfg, composeCfg)
	}, composeCfg, declared)
}

// buildAnthropicComposeClient turns the neutral endpoint configuration into a
// provider pool, constructing clients here only when caller-supplied client
// options leave the declarative path unable to express the endpoint.
func buildAnthropicComposeClient(cfg AnthropicConfig, composeCfg *composeConfig) (*anthropics.ComposeClient, error) {
	strategy := strategyOrFailover(cfg.Strategy)

	if len(composeCfg.anthropic.clientOpts) == 0 {
		return anthropics.NewFromEndpoints(strategy, toAnthropicSpecs(cfg.Endpoints), composeCfg.routerOpts...)
	}

	entries := make([]anthropics.ModelEntry, len(cfg.Endpoints))

	for i, e := range cfg.Endpoints {
		clientOpts := composeCfg.anthropic.clientOpts
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
			Name:       e.Model,
			Client:     anthropic.NewClient(e.APIKey, clientOpts...),
			Weight:     e.Weight,
			Alias:      e.Alias,
			Tags:       e.Tags,
			Capability: anthropicProviderCapability(e.Capabilities),
			Cost:       e.Cost,
			Latency:    e.Latency,
		}
	}

	return anthropics.NewComposeClient(strategy, entries, composeCfg.routerOpts...)
}

// toAnthropicSpecs maps the neutral endpoint configuration onto the provider's
// own endpoint specs.
func toAnthropicSpecs(endpoints []AnthropicEndpoint) []anthropics.EndpointSpec {
	specs := make([]anthropics.EndpointSpec, len(endpoints))

	for i, e := range endpoints {
		specs[i] = anthropics.EndpointSpec{
			BaseURL:    e.BaseURL,
			APIKey:     e.APIKey,
			Model:      e.Model,
			Alias:      e.Alias,
			Weight:     e.Weight,
			Tags:       e.Tags,
			Version:    e.Version,
			Beta:       e.Beta,
			Capability: anthropicProviderCapability(e.Capabilities),
			Cost:       e.Cost,
			Latency:    e.Latency,
		}
	}

	return specs
}

var (
	_ Caller                     = (*AnthropicMessagesComposeCaller)(nil)
	_ CapabilityProvider         = (*AnthropicMessagesComposeCaller)(nil)
	_ EndpointCapabilityProvider = (*AnthropicMessagesComposeCaller)(nil)
	_ AnthropicMessagesBackend   = (*anthropicComposeBackend)(nil)
)
