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
	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/router"
)

// Capability labels this package attaches to endpoints and requests. They are
// OpenAI-wire facts, so they are defined here rather than in the neutral core:
// the core compares them as opaque strings and gives them no meaning. The
// Anthropic wrapper defines its own set from its own protocol fields; the two
// pools never exchange a label.
const (
	// CapabilityTools marks function/tool calling.
	CapabilityTools = "tools"
	// CapabilityVision marks image input.
	CapabilityVision = "vision"
	// CapabilityResponses names the Responses method set in a capability error.
	// Unlike the others it is never declared on an entry: it is derived from
	// whether the entry's client implements [Responder].
	CapabilityResponses = "responses"
)

// Capability is the strong-typed contract an endpoint exposes to the router. It
// is declared by the endpoint (via ModelEntry.Capability / EndpointSpec
// .Capability) or, when a wrapped client implements CapabilityProvider, by the
// client itself. It is never inferred from Tags or a provider name, and it
// drives candidate filtering only — the router excludes incapable endpoints but
// never strips tools, rewrites the request, switches dialect, or downgrades.
//
// Filtering is opt-in: an endpoint that declares nothing is *unknown*, not
// *incapable*, and never participates in the filter. Only an explicit
// declaration — a non-nil ModelEntry.Capability or a CapabilityProvider client —
// can exclude an endpoint.
//
// It is translated into the neutral core's opaque label set at construction;
// the core never sees this type.
type Capability struct {
	// Tools marks the endpoint able to serve function/tool calls.
	Tools bool
	// Vision marks the endpoint able to accept image content parts.
	Vision bool
	// MaxContextTokens is the endpoint's context window. It is part of the
	// contract for future token-budget routing; this SDK does not estimate
	// token counts, so it is not used for filtering today.
	MaxContextTokens int
}

// CapabilityProvider is optionally implemented by a ChatCompleter (e.g. a
// provider's native client) that can declare its own Capability. A non-nil
// ModelEntry.Capability always overrides this declaration. Implementing it is
// itself a declaration: the returned Capability participates in filtering, so a
// zero value means "supports neither tools nor vision", not "unknown".
type CapabilityProvider interface {
	ComposeCapability() Capability
}

// declaresOf returns the entry's neutral label declaration, or nil when the
// entry declares nothing at all. An explicit ModelEntry.Capability wins;
// otherwise a client implementing CapabilityProvider declares it. With neither,
// the capability is *unknown* — the entry is left out of the filter rather than
// assumed incapable, so configurations that never declare a Capability route
// exactly as they did before capability filtering existed.
func declaresOf(e *ModelEntry) []string {
	declared := e.Capability

	if declared == nil {
		if cp, ok := e.Client.(CapabilityProvider); ok {
			from := cp.ComposeCapability()
			declared = &from
		}
	}

	if declared == nil {
		return nil
	}

	// Declare() is non-nil even when empty, which is what tells the core
	// "declares nothing" apart from "undeclared".
	labels := router.Declare()

	if declared.Tools {
		labels = append(labels, CapabilityTools)
	}

	if declared.Vision {
		labels = append(labels, CapabilityVision)
	}

	return labels
}

// chatRequires returns the labels a Chat Completions request demands, read from
// its own fields only.
func chatRequires(request *openai.ChatCompletionRequest) []string {
	var requires []string

	if chatRequiresTools(request) {
		requires = append(requires, CapabilityTools)
	}

	if chatRequiresVision(request) {
		requires = append(requires, CapabilityVision)
	}

	return requires
}

// chatRequiresTools reports whether the request needs a tools-capable endpoint:
// it defines tools, or sets an explicit tool_choice other than "none".
func chatRequiresTools(request *openai.ChatCompletionRequest) bool {
	if len(request.Tools) > 0 {
		return true
	}

	// ToolChoice is polymorphic (a string or an object). A nil or "none"
	// choice needs no tools; any other value does.
	if request.ToolChoice == nil {
		return false
	}

	if s, ok := request.ToolChoice.(string); ok && s == "none" {
		return false
	}

	return true
}

// chatRequiresVision reports whether any message carries an image part.
func chatRequiresVision(request *openai.ChatCompletionRequest) bool {
	for i := range request.Messages {
		for _, part := range request.Messages[i].Content.Parts() {
			if part.ImageURL != nil || part.Type == openai.ContentPartTypeImageURL {
				return true
			}
		}
	}

	return false
}

// chatOutputUnits is the output volume StrategyCost scales OutputPrice by. Token
// estimation is out of scope, so it is the request's own output cap when
// present and one unit otherwise.
func chatOutputUnits(request *openai.ChatCompletionRequest) float64 {
	if request.MaxCompletionTokens != nil {
		return float64(*request.MaxCompletionTokens)
	}

	if request.MaxTokens != nil { // fallback for pre-max_completion_tokens models
		return float64(*request.MaxTokens)
	}

	return 1
}

// responsesRequires returns the labels a Responses request demands. It reads
// the Responses wire type's own fields: nothing is translated from or to the
// Chat Completions shape.
func responsesRequires(request *openai.ResponsesRequest) []string {
	var requires []string

	if responsesRequireTools(request) {
		requires = append(requires, CapabilityTools)
	}

	if responsesRequireVision(request) {
		requires = append(requires, CapabilityVision)
	}

	return requires
}

// responsesRequireTools reads Responses' own tools/tool_choice fields. The
// hosted tools (web_search, file_search, …) live in the same Tools slice, so
// declaring any of them requires a tools-capable endpoint.
func responsesRequireTools(request *openai.ResponsesRequest) bool {
	if len(request.Tools) > 0 {
		return true
	}

	if request.ToolChoice == nil {
		return false
	}

	if s, ok := request.ToolChoice.(string); ok && s == "none" {
		return false
	}

	return true
}

// responsesRequireVision reports whether the structured input carries an
// input_image part. The scalar string form of `input` never does.
func responsesRequireVision(request *openai.ResponsesRequest) bool {
	for _, item := range request.Input.Items() {
		if item.Message == nil {
			continue
		}

		for _, part := range item.Message.Content.Parts() {
			if part.Type == openai.ResponseContentTypeInputImage || part.ImageURL != "" {
				return true
			}
		}
	}

	return false
}

// responsesOutputUnits mirrors chatOutputUnits over the Responses output cap.
func responsesOutputUnits(request *openai.ResponsesRequest) float64 {
	if request.MaxOutputTokens != nil {
		return float64(*request.MaxOutputTokens)
	}

	return 1
}
