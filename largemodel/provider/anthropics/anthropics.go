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

// Package anthropics dispatches Anthropic Messages calls across several
// Anthropic-compatible backends, with a stable active endpoint, in-call
// retries, health tracking, capability filtering and economic routing.
//
// It is the Anthropic half of a two-layer split: the operational machinery
// lives in the protocol-neutral [github.com/vogo/vage/largemodel/router] core, and
// this package binds it to `anthropic` types.
//
// A pool here is Anthropic-wire only, and this package neither imports nor is
// imported by [github.com/vogo/vage/largemodel/provider/openais]. There is no
// cross-protocol failover and no shared request model: what the two wrappers
// share is how a candidate is chosen and how health is recorded.
//
// The application-facing Caller adapter remains in the parent largemodel
// package. This package intentionally stays below that facade: it owns native
// wire codecs and routed backends, and does not import the parent package.
package anthropics

import (
	"context"
	"fmt"
	"slices"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/largemodel/router"
)

// Messenger is the method set a backend must provide to take part in dispatch.
// *anthropic.Client satisfies it, and so does a ComposeClient, which is what
// makes nesting work.
type Messenger interface {
	Messages(ctx context.Context, request *anthropic.MessagesRequest) (*anthropic.MessagesResponse, error)
	MessagesStream(ctx context.Context, request *anthropic.MessagesRequest) (*anthropic.MessageStream, error)
}

// ComposeClient dispatches Messages calls across multiple backends. It
// implements Messenger and can be nested.
type ComposeClient struct {
	entries []ModelEntry
	router  *router.Router
}

// NewComposeClient creates a ComposeClient with the given strategy and model
// entries. Options are the neutral core's ([router.WithRetryPolicy],
// [router.WithRecoverTime], [router.WithAttemptObserver], …).
func NewComposeClient(
	strategy router.Strategy, entries []ModelEntry, opts ...router.Option,
) (*ComposeClient, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("vage/largemodel/provider/anthropics: at least one model entry is required")
	}

	for i, e := range entries {
		if e.Client == nil {
			return nil, fmt.Errorf("vage/largemodel/provider/anthropics: entry %d (%q): client is nil", i, e.Name)
		}
	}

	// Own a copy before deriving aliases: the caller's slice and its elements
	// are never written back to.
	owned := slices.Clone(entries)
	deriveAliases(owned)

	endpoints := make([]router.Endpoint, len(owned))
	for i := range owned {
		endpoints[i] = router.Endpoint{
			Alias:    owned[i].Alias,
			Weight:   owned[i].Weight,
			Tags:     owned[i].Tags,
			Declares: declaresOf(&owned[i]),
			Cost:     owned[i].Cost,
			Latency:  owned[i].Latency,
		}
	}

	router, err := router.NewRouter(strategy, endpoints, opts...)
	if err != nil {
		return nil, err
	}

	return &ComposeClient{entries: owned, router: router}, nil
}

// Stats returns an immutable per-endpoint health snapshot, safe to call
// concurrently with dispatch.
func (c *ComposeClient) Stats() []router.EndpointStat { return c.router.Stats() }

// Messages sends a non-streaming Messages request, routing via the configured
// strategy.
func (c *ComposeClient) Messages(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessagesResponse, error) {
	return router.Dispatch(ctx, c.router, c.messagesCall(ctx, request, false),
		func(ctx context.Context, endpoint int) (*anthropic.MessagesResponse, error) {
			return c.entries[endpoint].Client.Messages(ctx, c.requestFor(endpoint, request))
		})
}

// MessagesStream sends a streaming Messages request, routing via the configured
// strategy. Only the call that opens the stream is covered by failover: once a
// backend has started streaming, a mid-stream error reaches the caller.
func (c *ComposeClient) MessagesStream(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessageStream, error) {
	return router.Dispatch(ctx, c.router, c.messagesCall(ctx, request, true),
		func(ctx context.Context, endpoint int) (*anthropic.MessageStream, error) {
			return c.entries[endpoint].Client.MessagesStream(ctx, c.requestFor(endpoint, request))
		})
}

// messagesCall describes a Messages dispatch to the neutral router: the labels
// this request needs and the output volume that scales cost ordering.
func (c *ComposeClient) messagesCall(ctx context.Context, request *anthropic.MessagesRequest, stream bool) router.Call {
	return router.Call{
		Requires:    messagesRequires(request),
		Eligible:    eligibleIndices(ctx, c.router),
		OutputUnits: messagesOutputUnits(request),
		Stream:      stream,
	}
}

func eligibleIndices(ctx context.Context, r *router.Router) []int {
	aliases := modelcore.EligibleAliases(ctx)
	if aliases == nil {
		return nil
	}

	indexByAlias := map[string]int{}
	for i, alias := range r.Aliases() {
		indexByAlias[alias] = i
	}

	out := make([]int, 0, len(aliases))
	for _, alias := range aliases {
		if idx, ok := indexByAlias[alias]; ok {
			out = append(out, idx)
		}
	}

	return out
}

// requestFor copies the request and overrides the model name for one endpoint,
// so the caller's request is untouched and each backend sees its own model. An
// empty entry name leaves the request's own model in place.
func (c *ComposeClient) requestFor(
	endpoint int, request *anthropic.MessagesRequest,
) *anthropic.MessagesRequest {
	r := *request
	if name := c.entries[endpoint].Name; name != "" {
		r.Model = name
	}

	return &r
}

// Compile-time checks: a ComposeClient is itself a backend, so compose clients
// nest; and the native Anthropic client can be used as one directly.
var (
	_ Messenger = (*ComposeClient)(nil)
	_ Messenger = (*anthropic.Client)(nil)
)
