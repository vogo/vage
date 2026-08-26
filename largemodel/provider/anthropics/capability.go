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

package anthropics

import (
	"encoding/json"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/router"
)

// Capability labels this package attaches to endpoints and requests. They are
// Anthropic-wire facts, so they are defined here rather than in the neutral
// core: the core compares them as opaque strings and gives them no meaning.
// The OpenAI wrapper defines its own set from its own protocol fields; the two
// pools never exchange a label, so the identical spelling is a coincidence of
// vocabulary, not a shared type.
const (
	// CapabilityTools marks tool use.
	CapabilityTools = "tools"
	// CapabilityVision marks image content blocks.
	CapabilityVision = "vision"
)

// Capability is the strong-typed contract an endpoint exposes to the router. It
// is declared by the endpoint (via ModelEntry.Capability / EndpointSpec
// .Capability) or, when a wrapped client implements CapabilityProvider, by the
// client itself. It drives candidate filtering only — the router excludes
// incapable endpoints but never strips tools, rewrites the request, or
// downgrades.
//
// Filtering is opt-in: an endpoint that declares nothing is *unknown*, not
// *incapable*, and never participates in the filter.
//
// It is translated into the neutral core's opaque label set at construction;
// the core never sees this type.
type Capability struct {
	// Tools marks the endpoint able to serve tool use.
	Tools bool
	// Vision marks the endpoint able to accept image content blocks.
	Vision bool
	// MaxContextTokens is the endpoint's context window. It is part of the
	// contract for future token-budget routing; this SDK does not estimate
	// token counts, so it is not used for filtering today.
	MaxContextTokens int
}

// CapabilityProvider is optionally implemented by a Messenger that can declare
// its own Capability. A non-nil ModelEntry.Capability always overrides this
// declaration. Implementing it is itself a declaration: a zero value means
// "supports neither tools nor vision", not "unknown".
type CapabilityProvider interface {
	ComposeCapability() Capability
}

// declaresOf returns the entry's neutral label declaration, or nil when the
// entry declares nothing at all.
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

// messagesRequires returns the labels a Messages request demands, read from
// Anthropic's own request fields only.
func messagesRequires(request *anthropic.MessagesRequest) []string {
	var requires []string

	if messagesRequireTools(request) {
		requires = append(requires, CapabilityTools)
	}

	if messagesRequireVision(request) {
		requires = append(requires, CapabilityVision)
	}

	return requires
}

// messagesRequireTools reports whether the request needs a tools-capable
// endpoint: it defines tools, or sets an explicit tool_choice other than
// "none". Anthropic's tool_choice is a typed object, not a polymorphic value.
func messagesRequireTools(request *anthropic.MessagesRequest) bool {
	if len(request.Tools) > 0 {
		return true
	}

	if request.ToolChoice == nil {
		return false
	}

	return request.ToolChoice.Type != anthropic.ToolChoiceTypeNone
}

// messagesRequireVision reports whether any message carries an image content
// block. Anthropic models message content as raw JSON — either a plain string
// or an array of blocks — so the array form is decoded here. Content this
// wrapper cannot decode is treated as carrying no image: routing never rejects
// a request the backend might well accept.
func messagesRequireVision(request *anthropic.MessagesRequest) bool {
	for i := range request.Messages {
		raw := request.Messages[i].Content
		if len(raw) == 0 || raw[0] != '[' {
			continue // the scalar string form carries no blocks
		}

		var blocks []anthropic.ContentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			continue
		}

		for j := range blocks {
			if blocks[j].Type == anthropic.ContentBlockTypeImage {
				return true
			}
		}
	}

	return false
}

// messagesOutputUnits is the output volume StrategyCost scales OutputPrice by.
// Anthropic requires max_tokens on every request, so it is normally present;
// a non-positive value counts as one unit.
func messagesOutputUnits(request *anthropic.MessagesRequest) float64 {
	if request.MaxTokens > 0 {
		return float64(request.MaxTokens)
	}

	return 1
}
