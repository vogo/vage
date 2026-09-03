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
	"maps"

	"github.com/vogo/vage/schema"
)

// Request is one model call as vage states it, before any vendor translation.
// It carries the parameters vage actually sets; each protocol's caller maps
// them onto that vendor's native request and drops what the vendor does not
// accept.
//
// Messages hold vendor-native wire forms already (see schema.Message), so a
// Request is only valid against a caller speaking the same protocol.
type Request struct {
	// Model is the vendor model identifier.
	Model string

	// Messages is the conversation, in vendor-native wire form.
	Messages []schema.Message

	// Tools are the tool definitions offered to the model.
	Tools []schema.ToolDef

	// Temperature, when set, overrides the vendor default sampling
	// temperature.
	Temperature *float64

	// MaxTokens caps the tokens the model may generate. It maps to OpenAI's
	// max_completion_tokens and to Anthropic's required max_tokens.
	MaxTokens *int

	// TopP, when set, is nucleus sampling. It maps to OpenAI and Anthropic
	// top_p. An explicit zero is sent; unset leaves the vendor default.
	TopP *float64

	// Seed, when set, requests deterministic sampling. It maps to OpenAI
	// Chat seed. Anthropic rejects it before the backend.
	Seed *int64

	// FrequencyPenalty, when set, maps to OpenAI Chat frequency_penalty.
	// Anthropic rejects it before the backend.
	FrequencyPenalty *float64

	// PresencePenalty, when set, maps to OpenAI Chat presence_penalty.
	// Anthropic rejects it before the backend.
	PresencePenalty *float64

	// ToolChoice, when set, is the cross-provider tool-selection policy and
	// wins over ToolDef.ForceUse. Unset keeps ForceUse's compatibility
	// behaviour. A named choice pointing at a tool not in Tools fails before
	// the backend.
	ToolChoice *ToolChoice

	// Stop are stop sequences that end generation.
	Stop []string

	// PromptCaching requests vendor prompt-cache breakpoints on the system
	// prompt and the final tool definition. It has an on-wire effect only for
	// Anthropic; OpenAI caches identical prefixes automatically.
	PromptCaching bool

	// ResponseSchema, when set, requires the model's final assistant text to
	// be raw JSON matching this JSON Schema. It carries the caller's schema
	// as-is — the same shape as schema.ToolDef.Parameters — and is treated as
	// a read-only value: vage never mutates or trims it.
	//
	// It constrains the model's text output only, not tool-call arguments, so
	// it may be set alongside Tools without interfering with each tool's own
	// Parameters schema.
	//
	// A protocol caller with a native structured-output mapping (OpenAI Chat,
	// Anthropic Messages) sends it as that vendor's own constraint field.
	// Codecs with no native mapping return a capability error unless
	// AllowPromptFallback has opted into a deterministic system instruction
	// (see degradeResponseSchemaPrompt). Either way this field itself does
	// not parse, validate, or strip code fences from the resulting text.
	ResponseSchema any

	// ProviderExtensions holds namespaced provider-private payloads. A codec
	// reads only its own namespace. Unknown namespaces, payloads that cannot
	// be JSON, and keys that collide with envelope or formal fields fail
	// before the backend. This is not a credential channel: API keys, base
	// URLs and headers still come only from Caller construction.
	ProviderExtensions map[string]any
}

// Clone returns a copy of the request with its slices duplicated, so a
// middleware may rewrite the copy without disturbing the caller's request.
// The messages themselves are shared: callers treat them as immutable.
// ResponseSchema is copied by value, like every ToolDef.Parameters: Clone
// does not deep-copy the schema object underneath it.
func (r *Request) Clone() *Request {
	c := *r

	if len(r.Messages) > 0 {
		c.Messages = make([]schema.Message, len(r.Messages))
		copy(c.Messages, r.Messages)
	}

	if len(r.Tools) > 0 {
		c.Tools = make([]schema.ToolDef, len(r.Tools))
		copy(c.Tools, r.Tools)
	}

	if len(r.Stop) > 0 {
		c.Stop = make([]string, len(r.Stop))
		copy(c.Stop, r.Stop)
	}

	c.ToolChoice = r.ToolChoice.clone()
	c.ProviderExtensions = cloneProviderExtensions(r.ProviderExtensions)

	return &c
}

func cloneProviderExtensions(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}

	out := make(map[string]any, len(in))
	for key, value := range in {
		switch typed := value.(type) {
		case []byte:
			out[key] = append([]byte(nil), typed...)
		case map[string]any:
			inner := make(map[string]any, len(typed))
			maps.Copy(inner, typed)
			out[key] = inner
		default:
			out[key] = value
		}
	}

	return out
}

// Response is one completed non-streaming model call, normalized off the
// vendor's native response. Message keeps its vendor-native wire form; the
// surrounding accounting is vage's own.
type Response struct {
	// ID is the vendor's identifier for this completion.
	ID string

	// Model is the model that actually served the call, as reported by the
	// vendor (it may differ from the requested alias).
	Model string

	// Message is the assistant reply in vendor-native wire form.
	Message schema.Message

	// FinishReason says why generation stopped, in vage's vocabulary.
	FinishReason FinishReason

	// Usage is the token accounting for this call.
	Usage schema.Usage
}

// FinishReason says why the model stopped generating. vage normalizes the
// vendors' differing vocabularies into this set; a value with no cross-vendor
// equivalent passes through verbatim, so callers should treat unrecognized
// values as opaque rather than assuming the set is closed.
type FinishReason string

// FinishReason constants with a cross-vendor meaning.
const (
	// FinishReasonStop means the model finished its turn normally.
	FinishReasonStop FinishReason = "stop"

	// FinishReasonLength means generation hit the token cap.
	FinishReasonLength FinishReason = "length"

	// FinishReasonToolCalls means the model is requesting tool calls and the
	// turn continues after they are answered.
	FinishReasonToolCalls FinishReason = "tool_calls"

	// FinishReasonContentFilter means the vendor withheld the content.
	FinishReasonContentFilter FinishReason = "content_filter"
)

// Chunk is one increment of a streaming response. Deltas are already
// normalized off the vendor's stream events, so the ReAct loop accumulates
// them the same way for every protocol.
type Chunk struct {
	// TextDelta is new assistant text, or empty when this chunk carries none.
	TextDelta string

	// ThinkingDelta is new reasoning text, or empty when this chunk carries
	// none.
	ThinkingDelta string

	// ToolCallDeltas are increments to tool calls. Vendors stream tool
	// arguments in fragments, and a single chunk may carry fragments of
	// several parallel calls; StreamAccumulator merges them by index.
	ToolCallDeltas []ToolCallDelta

	// FinishReason, when non-empty, marks the terminal chunk of the turn.
	FinishReason FinishReason

	// Usage, when non-nil, is the final token accounting the vendor reports
	// at the end of the stream.
	Usage *schema.Usage
}

// ToolCallDelta is a fragment of a streamed tool call. Index identifies which
// call in the turn the fragment belongs to, since vendors interleave
// fragments of parallel calls.
type ToolCallDelta struct {
	Index int

	// ID and Name arrive once, on the fragment that opens the call.
	ID   string
	Name string

	// ArgumentsDelta is a fragment of the JSON argument object, to be
	// appended to whatever arrived before it.
	ArgumentsDelta string
}

// Caller is the protocol-agnostic seam the middleware chain wraps. Each
// protocol has its own implementation owning the translation between these
// envelopes and that vendor's native wire types, so middlewares never see a
// vendor type.
//
// A Caller reports its protocol so middlewares and callers needing
// vendor-specific behaviour can branch without inspecting the client.
type Caller interface {
	// Protocol reports the wire protocol this caller speaks.
	Protocol() schema.Protocol

	// Call performs one non-streaming model call.
	Call(ctx context.Context, req *Request) (*Response, error)

	// CallStream performs one streaming model call. The returned Stream is
	// owned by this method's caller, which must Close it exactly once.
	CallStream(ctx context.Context, req *Request) (*Stream, error)
}

// CallerFunc adapts a pair of functions to Caller. It is the building block
// middlewares use to wrap the next caller while preserving its protocol.
type CallerFunc struct {
	// Proto is reported by Protocol; middlewares propagate the wrapped
	// caller's value so the protocol survives the whole chain.
	Proto schema.Protocol

	// Chat handles non-streaming calls.
	Chat func(ctx context.Context, req *Request) (*Response, error)

	// ChatStream handles streaming calls.
	ChatStream func(ctx context.Context, req *Request) (*Stream, error)

	capabilities         func(context.Context, *Request) (Capabilities, error)
	endpointCapabilities func() []EndpointCapability
}

// DelegateCaller copies protocol and capability seams from next so a
// middleware wrapper stays a CapabilityProvider when the inner caller is.
func DelegateCaller(next Caller) *CallerFunc {
	cf := &CallerFunc{Proto: next.Protocol()}
	if p, ok := next.(CapabilityProvider); ok {
		cf.capabilities = p.Capabilities
	}

	if p, ok := next.(EndpointCapabilityProvider); ok {
		cf.endpointCapabilities = p.EndpointCapabilities
	}

	return cf
}

// BindCaller is DelegateCaller plus the Chat/ChatStream functions a middleware
// supplies.
func BindCaller(
	next Caller,
	chat func(context.Context, *Request) (*Response, error),
	stream func(context.Context, *Request) (*Stream, error),
) *CallerFunc {
	cf := DelegateCaller(next)
	cf.Chat = chat
	cf.ChatStream = stream

	return cf
}

// Protocol implements Caller.
func (c *CallerFunc) Protocol() schema.Protocol { return c.Proto }

// Call implements Caller.
func (c *CallerFunc) Call(ctx context.Context, req *Request) (*Response, error) {
	return c.Chat(ctx, req)
}

// CallStream implements Caller.
func (c *CallerFunc) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return c.ChatStream(ctx, req)
}

// Capabilities implements CapabilityProvider when the wrapper was built with
// DelegateCaller around a provider; otherwise the result is unknown.
func (c *CallerFunc) Capabilities(ctx context.Context, req *Request) (Capabilities, error) {
	if c.capabilities != nil {
		return c.capabilities(ctx, req)
	}

	return Capabilities{}, nil
}

// EndpointCapabilities implements EndpointCapabilityProvider when the inner
// caller does.
func (c *CallerFunc) EndpointCapabilities() []EndpointCapability {
	if c.endpointCapabilities != nil {
		return c.endpointCapabilities()
	}

	return nil
}

var (
	_ Caller                     = (*CallerFunc)(nil)
	_ CapabilityProvider         = (*CallerFunc)(nil)
	_ EndpointCapabilityProvider = (*CallerFunc)(nil)
)
