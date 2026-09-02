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

	"github.com/vogo/vage/largemodel/router"
)

// ComposeCaller is a Caller that reaches its endpoints through a router pool,
// so it can report what the pool knows about them. It is what [NewCaller] and
// [BuildCaller] return: a generic entry point needs one return type across
// both protocols, and endpoint health is the part of the concrete callers
// worth keeping at that width.
//
// The concrete types behind it — [OpenAIChatComposeCaller] and
// [AnthropicMessagesComposeCaller] — stay exported for code that needs more.
type ComposeCaller interface {
	Caller

	// EndpointStats reports endpoint health merged across the caller's pools.
	EndpointStats() []router.EndpointStat
}

// callerEndpoint is the set of single-endpoint specs [NewCaller] accepts. The
// type argument is what picks the protocol, so an endpoint and a wire format
// that disagree is a compile error rather than a runtime surprise.
type callerEndpoint interface {
	OpenAIEndpoint | AnthropicEndpoint
}

// callerConfig is the set of multi-endpoint configurations [BuildCaller]
// accepts, selecting the protocol the same way callerEndpoint does.
type callerConfig interface {
	OpenAIConfig | AnthropicConfig
}

// NewCaller builds a Caller over a single endpoint. It is [BuildCaller] for
// the common case, saving the caller a config struct wrapping a one-element
// slice; a pool of one gets the same retry and health behaviour as a larger
// pool. Reach for [BuildCaller] when there is a second endpoint or a
// non-default [Strategy] to declare.
//
// The protocol comes from the argument's type — largemodel never guesses one
// from a base URL or an API key:
//
//	caller, err := largemodel.NewCaller(largemodel.OpenAIEndpoint{APIKey: key})
//	caller, err := largemodel.NewCaller(largemodel.AnthropicEndpoint{APIKey: key})
//
// An empty Alias defaults to [DefaultEndpointAlias]. Aliases are the
// operational identity used in health snapshots and routing errors and are
// required deeper down, but a lone endpoint has nothing to be distinguished
// from, so naming it is the caller's option rather than their obligation.
func NewCaller[E callerEndpoint](endpoint E, opts ...CallerOption) (ComposeCaller, error) {
	switch ep := any(endpoint).(type) {
	case OpenAIEndpoint:
		if ep.Alias == "" {
			ep.Alias = DefaultEndpointAlias
		}

		return newOpenAIChatCallerFromConfig(OpenAIConfig{Endpoints: []OpenAIEndpoint{ep}}, opts...)
	case AnthropicEndpoint:
		if ep.Alias == "" {
			ep.Alias = DefaultEndpointAlias
		}

		return newAnthropicMessagesCallerFromConfig(AnthropicConfig{Endpoints: []AnthropicEndpoint{ep}}, opts...)
	default:
		// Unreachable: callerEndpoint admits no other type.
		return nil, fmt.Errorf("vage: unsupported endpoint type %T", endpoint)
	}
}

// BuildCaller builds a Caller over the endpoints and selection strategy
// described by cfg, with the protocol chosen by cfg's type:
//
//	caller, err := largemodel.BuildCaller(largemodel.OpenAIConfig{...})
//	caller, err := largemodel.BuildCaller(largemodel.AnthropicConfig{...})
//
// There is no cross-protocol pool: an OpenAI config and an Anthropic config
// build separate callers, with no shared request to hand between them.
func BuildCaller[C callerConfig](cfg C, opts ...CallerOption) (ComposeCaller, error) {
	switch c := any(cfg).(type) {
	case OpenAIConfig:
		return newOpenAIChatCallerFromConfig(c, opts...)
	case AnthropicConfig:
		return newAnthropicMessagesCallerFromConfig(c, opts...)
	default:
		// Unreachable: callerConfig admits no other type.
		return nil, fmt.Errorf("vage: unsupported caller config type %T", cfg)
	}
}

// WrapCaller wraps a backend the caller built itself — a bare *openai.Client,
// a hand-assembled compose client, a test double. The backend is used as-is:
// nothing is routed, retried or health-tracked around it, which is why the
// result is a plain [Caller] rather than a [ComposeCaller], and if it is a
// provider pool it still serves one call at a time and must not be shared with
// agents that run in parallel.
//
// Unlike [NewCaller] and [BuildCaller] this dispatches at run time. The
// protocol backends are interfaces with methods, and Go admits no such type in
// a union, so there is no constraint that could make the choice a compile
// error. The two method sets do not overlap, so the choice is still
// determined: a backend implementing neither is [ErrUnsupportedBackend], one
// implementing both is [ErrAmbiguousBackend] — a wire format cannot be picked
// for it, and picking one silently would be worse than saying so.
func WrapCaller(backend any) (Caller, error) {
	if backend == nil {
		return nil, ErrNoBackend
	}

	openAIBackend, isOpenAI := backend.(OpenAIChatBackend)
	anthropicBackend, isAnthropic := backend.(AnthropicMessagesBackend)

	switch {
	case isOpenAI && isAnthropic:
		return nil, fmt.Errorf("%w: %T", ErrAmbiguousBackend, backend)
	case isOpenAI:
		return newOpenAIChatCaller(openAIBackend), nil
	case isAnthropic:
		return newAnthropicMessagesCaller(anthropicBackend), nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnsupportedBackend, backend)
	}
}
