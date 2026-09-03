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

// Package modelcore is the shared contract point between the largemodel root
// package and the protocol codecs under largemodel/provider.
//
// It exists to break an import cycle. The root package owns the public call
// envelopes and imports every provider, so a provider must not import the root
// package back — yet a provider codec has to speak in something the root
// package can hand it. That something lives here: a narrow, vendor-neutral
// mirror of what the public envelopes already express, plus the normalized
// failure classification a codec produces.
//
// Nothing here is part of the public API and nothing here interprets a wire
// format. Vendor knowledge belongs in the provider packages; the envelope
// contract belongs to largemodel. This package is only the seam between them,
// and callers keep using the root package's exported names.
package modelcore

import (
	"context"

	"github.com/vogo/vage/schema"
)

// Request is one model call in canonical form, mirroring largemodel.Request
// field for field. The root package converts between the two mechanically; a
// codec reads it and owns every decision about how it reaches the wire.
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

	// MaxTokens caps the tokens the model may generate. How a vendor spells
	// that cap, and whether it is mandatory, is the codec's business.
	MaxTokens *int

	// TopP is nucleus sampling when set.
	TopP *float64

	// Seed requests deterministic sampling when set.
	Seed *int64

	// FrequencyPenalty maps to vendors that accept it; others must reject it.
	FrequencyPenalty *float64

	// PresencePenalty maps to vendors that accept it; others must reject it.
	PresencePenalty *float64

	// ToolChoice, when set, is the cross-provider tool-selection policy and
	// wins over ToolDef.ForceUse.
	ToolChoice *ToolChoice

	// Stop are stop sequences that end generation.
	Stop []string

	// PromptCaching requests vendor prompt-cache breakpoints. Whether it has
	// any wire effect is the codec's business.
	PromptCaching bool

	// ResponseSchema, when set, constrains the final assistant text to raw
	// JSON matching this schema. It is read-only: a codec never mutates or
	// trims it.
	ResponseSchema any

	// ProviderExtensions holds namespaced provider-private payloads. A codec
	// reads only its own namespace.
	ProviderExtensions map[string]any
}

// ToolChoice is the cross-provider tool-selection policy, mirroring
// largemodel.ToolChoice.
type ToolChoice struct {
	Mode string
	Name string
}

// Result is one completed non-streaming call, already normalized off the
// vendor's native response. It mirrors largemodel.Response, except that
// FinishReason is a plain string: the vendor vocabulary is mapped by the codec,
// and the root package only re-types the result.
type Result struct {
	// ID is the vendor's identifier for this completion.
	ID string

	// Model is the model that actually served the call.
	Model string

	// Message is the assistant reply in vendor-native wire form.
	Message schema.Message

	// FinishReason says why generation stopped, already in vage's vocabulary.
	// A reason with no cross-vendor equivalent passes through verbatim.
	FinishReason string

	// Usage is the token accounting for this call.
	Usage schema.Usage
}

// Chunk is one increment of a streaming response, already decoded off the
// vendor's stream events. It mirrors largemodel.Chunk.
type Chunk struct {
	// TextDelta is new assistant text, or empty when this chunk carries none.
	TextDelta string

	// ThinkingDelta is new reasoning text, or empty when this chunk carries
	// none.
	ThinkingDelta string

	// ToolCallDeltas are increments to tool calls, merged by index upstream.
	ToolCallDeltas []ToolCallDelta

	// FinishReason, when non-empty, marks the terminal chunk of the turn.
	FinishReason string

	// Usage, when non-nil, is the final token accounting for the turn.
	Usage *schema.Usage
}

// ToolCallDelta is a fragment of a streamed tool call, mirroring
// largemodel.ToolCallDelta.
type ToolCallDelta struct {
	Index int

	// ID and Name arrive once, on the fragment that opens the call.
	ID   string
	Name string

	// ArgumentsDelta is a fragment of the JSON argument object.
	ArgumentsDelta string
}

// Stream is a decoded event source a codec hands back: the pull function and
// the underlying release. The lifecycle guarantees callers depend on — Close
// exactly once, terminal usage capture, unblocking a Recv in flight — stay in
// largemodel.Stream, which wraps these two functions.
//
// Recv must return io.EOF exactly once when the turn is complete.
type Stream struct {
	Recv  func() (*Chunk, error)
	Close func() error
}

// Codec is the whole contract a protocol provider exposes to the root package.
// One implementation owns one vendor protocol end to end: request assembly,
// response normalization, stream decoding and error classification.
type Codec interface {
	// Protocol reports the wire protocol this codec speaks.
	Protocol() schema.Protocol

	// Call performs one non-streaming model call.
	Call(ctx context.Context, req *Request) (*Result, error)

	// CallStream performs one streaming model call.
	CallStream(ctx context.Context, req *Request) (*Stream, error)

	// NativeStructuredOutput reports whether this codec maps ResponseSchema
	// onto a vendor field. Codecs that return false must not send the schema
	// as a native constraint; the facade either prompt-degrades (opt-in) or
	// returns a capability error.
	NativeStructuredOutput() bool
}
