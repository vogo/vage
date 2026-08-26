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

// Package openais dispatches OpenAI-wire calls across several
// OpenAI-compatible backends, with a stable active endpoint, in-call retries,
// health tracking, capability filtering and economic routing.
//
// It is the OpenAI half of a two-layer split: the operational machinery lives
// in the protocol-neutral [github.com/vogo/vage/largemodel/router] core, and this
// package binds it to `openai` types. Two interaction forms are
// covered, each with its own method set over the same endpoint pool and the
// same health state:
//
//   - Chat Completions — [ComposeClient.ChatCompletions] / [ComposeClient.ChatCompletionsStream]
//   - Responses        — [ComposeClient.Responses] / [ComposeClient.ResponsesStream]
//
// A pool here is OpenAI-wire only. Composing Anthropic backends is
// [github.com/vogo/vage/largemodel/provider/anthropics] — a separate pool over the same
// routing core, never a mixed one: what the two share is how a candidate is
// chosen and how health is recorded, not what a request looks like.
//
// The application-facing Caller adapter remains in the parent largemodel
// package. This package intentionally stays below that facade: it owns native
// wire codecs and routed backends, and does not import the parent package.
package openais

import (
	"context"
	"fmt"
	"slices"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/router"
)

// ChatCompleter is the method set a backend must provide to take part in Chat
// Completions dispatch. *openai.Client satisfies it, and so does a
// ComposeClient, which is what makes nesting work.
type ChatCompleter interface {
	ChatCompletions(ctx context.Context, request *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error)
	ChatCompletionsStream(ctx context.Context, request *openai.ChatCompletionRequest) (*openai.ChatCompletionStream, error)
}

// Responder is the method set a backend must provide to take part in Responses
// dispatch. It is a separate, narrow method set rather than a widening of
// ChatCompleter: an entry whose client implements only ChatCompleter keeps
// working for chat and simply does not take part in Responses routing.
type Responder interface {
	Responses(ctx context.Context, request *openai.ResponsesRequest) (*openai.Response, error)
	ResponsesStream(ctx context.Context, request *openai.ResponsesRequest) (*openai.ResponseStream, error)
}

// ComposeClient dispatches OpenAI-wire calls across multiple backends. It
// implements both ChatCompleter and Responder, so pools nest.
type ComposeClient struct {
	entries []ModelEntry
	router  *router.Router
	// responders lists the entry indices whose client also implements Responder.
	// It is computed once at construction and passed as Call.Eligible.
	responders []int
	// responderClients mirrors entries: non-nil at every responders index so
	// Responses dispatch never type-asserts on the hot path.
	responderClients []Responder
}

// NewComposeClient creates a ComposeClient with the given strategy and model
// entries. Options are the neutral core's ([router.WithRetryPolicy],
// [router.WithRecoverTime], [router.WithAttemptObserver], …) — a pool's
// operational behaviour is described the same way whatever protocol it serves.
func NewComposeClient(
	strategy router.Strategy, entries []ModelEntry, opts ...router.Option,
) (*ComposeClient, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("vage/largemodel/provider/openais: at least one model entry is required")
	}

	for i, e := range entries {
		if e.Client == nil {
			return nil, fmt.Errorf("vage/largemodel/provider/openais: entry %d (%q): client is nil", i, e.Name)
		}
	}

	// Own a copy before deriving aliases: the caller's slice and its elements are
	// never written back to, matching the "clone first, never touch the caller's
	// object" rule the request path follows.
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

	responderClients := make([]Responder, len(owned))

	var responders []int

	for i := range owned {
		if responder, ok := owned[i].Client.(Responder); ok {
			responders = append(responders, i)
			responderClients[i] = responder
		}
	}

	return &ComposeClient{
		entries:          owned,
		router:           router,
		responders:       responders,
		responderClients: responderClients,
	}, nil
}

// Stats returns an immutable per-endpoint health snapshot, safe to call
// concurrently with dispatch. Both interaction forms share one pool, so a
// Responses failure is visible to Chat routing and vice versa.
func (c *ComposeClient) Stats() []router.EndpointStat { return c.router.Stats() }

// ChatCompletions sends a non-streaming Chat Completions request, routing via
// the configured strategy.
func (c *ComposeClient) ChatCompletions(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionResponse, error) {
	return router.Dispatch(ctx, c.router, chatCall(request, false),
		func(ctx context.Context, endpoint int) (*openai.ChatCompletionResponse, error) {
			return c.entries[endpoint].Client.ChatCompletions(ctx, c.chatRequestFor(endpoint, request))
		})
}

// ChatCompletionsStream sends a streaming Chat Completions request, routing via
// the configured strategy. Only the call that opens the stream is covered by
// failover: once a backend has started streaming, a mid-stream error reaches
// the caller.
func (c *ComposeClient) ChatCompletionsStream(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionStream, error) {
	return router.Dispatch(ctx, c.router, chatCall(request, true),
		func(ctx context.Context, endpoint int) (*openai.ChatCompletionStream, error) {
			return c.entries[endpoint].Client.ChatCompletionsStream(ctx, c.chatRequestFor(endpoint, request))
		})
}

// Responses sends a non-streaming Responses request, routing via the configured
// strategy. Only entries whose client implements Responder take part; when no
// entry does, the call fails with a *router.CapabilityError before any
// network I/O.
func (c *ComposeClient) Responses(
	ctx context.Context, request *openai.ResponsesRequest,
) (*openai.Response, error) {
	call, err := c.responsesCall(request, false)
	if err != nil {
		return nil, err
	}

	return router.Dispatch(ctx, c.router, call,
		func(ctx context.Context, endpoint int) (*openai.Response, error) {
			return c.responderClients[endpoint].Responses(ctx, c.responsesRequestFor(endpoint, request))
		})
}

// ResponsesStream sends a streaming Responses request, routing via the
// configured strategy. As with chat, only stream establishment is covered by
// failover.
func (c *ComposeClient) ResponsesStream(
	ctx context.Context, request *openai.ResponsesRequest,
) (*openai.ResponseStream, error) {
	call, err := c.responsesCall(request, true)
	if err != nil {
		return nil, err
	}

	return router.Dispatch(ctx, c.router, call,
		func(ctx context.Context, endpoint int) (*openai.ResponseStream, error) {
			return c.responderClients[endpoint].ResponsesStream(ctx, c.responsesRequestFor(endpoint, request))
		})
}

// chatCall describes a Chat Completions dispatch to the neutral router: the
// labels this request needs and the output volume that scales cost ordering.
func chatCall(request *openai.ChatCompletionRequest, stream bool) router.Call {
	return router.Call{
		Requires:    chatRequires(request),
		OutputUnits: chatOutputUnits(request),
		Stream:      stream,
	}
}

// responsesCall describes a Responses dispatch, restricted to entries that can
// serve one. It fails fast when no entry can.
func (c *ComposeClient) responsesCall(request *openai.ResponsesRequest, stream bool) (router.Call, error) {
	if len(c.responders) == 0 {
		return router.Call{}, &router.CapabilityError{
			Required:   []string{CapabilityResponses},
			Considered: c.router.Aliases(),
		}
	}

	return router.Call{
		Requires:    responsesRequires(request),
		Eligible:    c.responders,
		OutputUnits: responsesOutputUnits(request),
		Stream:      stream,
	}, nil
}

// chatRequestFor copies the request and overrides the model name for one
// endpoint, so the caller's request is untouched and each backend sees its own
// model. An empty entry name leaves the request's own model in place.
func (c *ComposeClient) chatRequestFor(
	endpoint int, request *openai.ChatCompletionRequest,
) *openai.ChatCompletionRequest {
	r := *request
	r.Model = c.modelFor(endpoint, r.Model)

	return &r
}

// responsesRequestFor is chatRequestFor for the Responses wire type. The two
// are deliberately separate: each reads its own protocol's Model field and
// nothing is translated between them.
func (c *ComposeClient) responsesRequestFor(
	endpoint int, request *openai.ResponsesRequest,
) *openai.ResponsesRequest {
	r := *request
	r.Model = c.modelFor(endpoint, r.Model)

	return &r
}

// modelFor returns the entry's Name when set, otherwise the request's own model.
func (c *ComposeClient) modelFor(endpoint int, fallback string) string {
	if name := c.entries[endpoint].Name; name != "" {
		return name
	}

	return fallback
}

// Compile-time checks: a ComposeClient is itself a backend for both interaction
// forms, so compose clients nest; and the native OpenAI client can be used as
// one directly.
var (
	_ ChatCompleter = (*ComposeClient)(nil)
	_ Responder     = (*ComposeClient)(nil)
	_ ChatCompleter = (*openai.Client)(nil)
	_ Responder     = (*openai.Client)(nil)
)
