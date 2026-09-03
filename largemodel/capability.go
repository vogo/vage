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

	"github.com/vogo/vage/schema"
)

// SupportLevel is how completely an endpoint/model can honour one capability.
// The zero value is unknown: nothing was declared and a query could not
// conclude. Strict checks treat unknown and unsupported the same — not met.
type SupportLevel string

const (
	// SupportUnknown means the capability was not declared. It is also the
	// zero value, so an omitted field stays unknown rather than unsupported.
	SupportUnknown SupportLevel = ""
	// SupportUnsupported means the endpoint has declared it cannot provide
	// the capability.
	SupportUnsupported SupportLevel = "unsupported"
	// SupportPrompt means the framework can only approximate the capability
	// with a prompt constraint. It is legal only for structured output.
	SupportPrompt SupportLevel = "prompt"
	// SupportNative means the endpoint can receive the corresponding native
	// protocol field.
	SupportNative SupportLevel = "native"
)

// Valid reports whether l is a known support level, including the zero
// unknown value.
func (l SupportLevel) Valid() bool {
	switch l {
	case SupportUnknown, SupportUnsupported, SupportPrompt, SupportNative:
		return true
	default:
		return false
	}
}

func (l SupportLevel) String() string {
	if l == SupportUnknown {
		return "unknown"
	}

	return string(l)
}

func (l SupportLevel) rank() int {
	switch l {
	case SupportNative:
		return 3
	case SupportPrompt:
		return 2
	default:
		return 0
	}
}

// Meets reports whether have satisfies a required minimum. An empty/unknown
// requirement is not a constraint. Unknown and unsupported never satisfy
// prompt or native.
func (have SupportLevel) Meets(need SupportLevel) bool {
	if need == SupportUnknown {
		return true
	}

	return have.rank() >= need.rank() && need.rank() > 0
}

// Modality names an input content kind the capability contract covers.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityFile  Modality = "file"
)

func (m Modality) Valid() bool {
	switch m {
	case ModalityText, ModalityImage, ModalityFile:
		return true
	default:
		return false
	}
}

// Capabilities is the declared fact about one endpoint/model. Zero values are
// unknown. It is never inferred from a model name, URL, tag, or a successful
// call.
type Capabilities struct {
	Modalities       map[Modality]SupportLevel
	StructuredOutput SupportLevel
	ToolCalling      SupportLevel
}

// Validate reports illegal combinations: prompt is only legal on structured
// output, and every modality key must be a known input kind.
func (c Capabilities) Validate() error {
	if err := validateLevel("structured_output", c.StructuredOutput, true); err != nil {
		return err
	}

	if err := validateLevel("tool_calling", c.ToolCalling, false); err != nil {
		return err
	}

	for mod, level := range c.Modalities {
		if !mod.Valid() {
			return fmt.Errorf("vage: unknown input modality %q", mod)
		}

		if err := validateLevel(string(mod), level, false); err != nil {
			return err
		}
	}

	return nil
}

func validateLevel(name string, level SupportLevel, promptOK bool) error {
	if !level.Valid() {
		return fmt.Errorf("vage: invalid %s support level %q", name, level)
	}

	if level == SupportPrompt && !promptOK {
		return fmt.Errorf("vage: prompt support is not valid for %s", name)
	}

	return nil
}

func (c Capabilities) modality(m Modality) SupportLevel {
	if c.Modalities == nil {
		return SupportUnknown
	}

	return c.Modalities[m]
}

// Requirements is the minimum support a call needs. Callers may add
// requirements; they cannot cancel ones the request itself implies.
type Requirements struct {
	Modalities       map[Modality]SupportLevel
	StructuredOutput SupportLevel
	ToolCalling      SupportLevel
}

// Validate reports illegal requirement combinations.
func (r Requirements) Validate() error {
	return Capabilities(r).Validate()
}

func (r Requirements) modality(m Modality) SupportLevel {
	if r.Modalities == nil {
		return SupportUnknown
	}

	return r.Modalities[m]
}

// Merge returns a requirement set that is at least as strong as both r and
// extra. The stronger (higher) support level wins for each field.
func (r Requirements) Merge(extra Requirements) Requirements {
	out := Requirements{
		StructuredOutput: stronger(r.StructuredOutput, extra.StructuredOutput),
		ToolCalling:      stronger(r.ToolCalling, extra.ToolCalling),
	}

	mods := map[Modality]SupportLevel{}
	for _, src := range []map[Modality]SupportLevel{r.Modalities, extra.Modalities} {
		for mod, level := range src {
			mods[mod] = stronger(mods[mod], level)
		}
	}

	if len(mods) > 0 {
		out.Modalities = mods
	}

	return out
}

func stronger(a, b SupportLevel) SupportLevel {
	if a.rank() >= b.rank() {
		return a
	}

	return b
}

// Unsatisfied returns the names of requirements have does not meet.
func (r Requirements) Unsatisfied(have Capabilities) []string {
	var missing []string

	if !have.StructuredOutput.Meets(r.StructuredOutput) {
		missing = append(missing, "structured_output")
	}

	if !have.ToolCalling.Meets(r.ToolCalling) {
		missing = append(missing, "tool_calling")
	}

	seen := map[Modality]bool{}
	for _, src := range []map[Modality]SupportLevel{r.Modalities, have.Modalities} {
		for mod := range src {
			if seen[mod] {
				continue
			}

			seen[mod] = true
			if !have.modality(mod).Meets(r.modality(mod)) {
				missing = append(missing, string(mod))
			}
		}
	}

	return missing
}

// DeriveRequirements reads the lowest requirements implied by req itself:
// image/file parts, tools / a non-none ToolChoice, and a ResponseSchema.
func DeriveRequirements(req *Request) Requirements {
	if req == nil {
		return Requirements{}
	}

	var r Requirements

	for i := range req.Messages {
		for _, part := range req.Messages[i].Parts() {
			switch part.Type {
			case schema.MessagePartImage:
				r = r.Merge(Requirements{Modalities: map[Modality]SupportLevel{ModalityImage: SupportNative}})
			case schema.MessagePartFile:
				r = r.Merge(Requirements{Modalities: map[Modality]SupportLevel{ModalityFile: SupportNative}})
			}
		}
	}

	if len(req.Tools) > 0 || toolChoiceRequiresTools(req.ToolChoice) {
		r.ToolCalling = SupportNative
	}

	if req.ResponseSchema != nil {
		r.StructuredOutput = SupportNative
	}

	return r
}

func toolChoiceRequiresTools(choice *ToolChoice) bool {
	if choice == nil {
		return false
	}

	return choice.Mode != ToolChoiceNone
}

// CapabilityProvider is optionally implemented by a Caller that can declare
// what the effective model of a request can do. The query is local: it must
// not call a model. A Caller that does not implement it is treated as unknown.
type CapabilityProvider interface {
	Capabilities(ctx context.Context, req *Request) (Capabilities, error)
}

// EndpointCapability is one compose-caller candidate's declared capability.
type EndpointCapability struct {
	Alias        string
	Model        string
	Capabilities Capabilities
}

// EndpointCapabilityProvider is optionally implemented by a multi-endpoint
// Caller. Strict filtering keeps a candidate only when that one candidate
// meets every requirement; capabilities are never unioned across endpoints.
type EndpointCapabilityProvider interface {
	EndpointCapabilities() []EndpointCapability
}

func capabilitiesOf(caller Caller, ctx context.Context, req *Request) (Capabilities, error) {
	if p, ok := caller.(CapabilityProvider); ok {
		return p.Capabilities(ctx, req)
	}

	return Capabilities{}, nil
}

func endpointCapabilitiesOf(caller Caller) []EndpointCapability {
	if p, ok := caller.(EndpointCapabilityProvider); ok {
		return p.EndpointCapabilities()
	}

	return nil
}
