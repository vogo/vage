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

	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// RequireNativeCapabilities wraps next so Call and CallStream fail closed
// before any backend I/O unless one routable candidate natively meets every
// requirement implied by the request plus extra. Extra cannot cancel implied
// requirements.
//
// Pairing this with AllowPromptFallback lowers only the structured-output
// floor to prompt. Passing extra that explicitly requires native structured
// output together with AllowPromptFallback is a configuration conflict.
func RequireNativeCapabilities(next Caller, extra ...Requirements) Caller {
	merged, err := mergeExtraRequirements(extra)
	if err != nil {
		return &capabilityPolicyCaller{next: next, strict: true, configErr: err}
	}

	return foldPolicy(next, capabilityPolicy{strict: true, extra: merged})
}

// AllowPromptFallback wraps next so structured output may be honoured with a
// deterministic prompt constraint when the codec has no native mapping. Other
// capabilities are unchanged. Prompt fallback is never implied.
func AllowPromptFallback(next Caller) Caller {
	return foldPolicy(next, capabilityPolicy{promptFallback: true})
}

type capabilityPolicy struct {
	strict         bool
	promptFallback bool
	extra          Requirements
	configErr      error
}

type capabilityPolicyCaller struct {
	next           Caller
	strict         bool
	promptFallback bool
	extra          Requirements
	configErr      error
}

func foldPolicy(next Caller, add capabilityPolicy) Caller {
	if existing, ok := next.(*capabilityPolicyCaller); ok {
		out := *existing
		out.strict = existing.strict || add.strict
		out.promptFallback = existing.promptFallback || add.promptFallback
		out.extra = existing.extra.Merge(add.extra)
		if out.configErr == nil {
			out.configErr = add.configErr
		}
		if out.configErr == nil {
			out.configErr = out.validatePolicy()
		}

		return &out
	}

	out := &capabilityPolicyCaller{
		next:           next,
		strict:         add.strict,
		promptFallback: add.promptFallback,
		extra:          add.extra,
		configErr:      add.configErr,
	}
	if out.configErr == nil {
		out.configErr = out.validatePolicy()
	}

	return out
}

func mergeExtraRequirements(extra []Requirements) (Requirements, error) {
	var merged Requirements
	for _, req := range extra {
		if err := req.Validate(); err != nil {
			return Requirements{}, err
		}

		merged = merged.Merge(req)
	}

	return merged, nil
}

func (c *capabilityPolicyCaller) validatePolicy() error {
	if c.promptFallback && c.extra.StructuredOutput == SupportNative {
		return fmt.Errorf("vage: AllowPromptFallback conflicts with an explicit native structured-output requirement")
	}

	return c.extra.Validate()
}

func (c *capabilityPolicyCaller) Protocol() schema.Protocol { return c.next.Protocol() }

func (c *capabilityPolicyCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	ctx, callReq, err := c.prepare(ctx, req)
	if err != nil {
		return nil, err
	}

	return c.next.Call(ctx, callReq)
}

func (c *capabilityPolicyCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	ctx, callReq, err := c.prepare(ctx, req)
	if err != nil {
		return nil, err
	}

	return c.next.CallStream(ctx, callReq)
}

func (c *capabilityPolicyCaller) Capabilities(ctx context.Context, req *Request) (Capabilities, error) {
	return capabilitiesOf(c.next, ctx, req)
}

func (c *capabilityPolicyCaller) EndpointCapabilities() []EndpointCapability {
	return endpointCapabilitiesOf(c.next)
}

func (c *capabilityPolicyCaller) prepare(ctx context.Context, req *Request) (context.Context, *Request, error) {
	if c.configErr != nil {
		return ctx, nil, c.configErr
	}

	needed := DeriveRequirements(req).Merge(c.extra)
	if c.promptFallback && needed.StructuredOutput == SupportNative {
		needed.StructuredOutput = SupportPrompt
	}

	if err := needed.Validate(); err != nil {
		return ctx, nil, err
	}

	if c.promptFallback {
		ctx = modelcore.WithPromptFallback(ctx)
	}

	if !c.strict && !c.promptFallback {
		return ctx, req, nil
	}

	if !c.strict {
		return ctx, req, nil
	}

	model := ""
	if req != nil {
		model = req.Model
	}

	candidates := endpointCapabilitiesOf(c.next)
	if len(candidates) > 0 {
		var eligible []string
		for _, candidate := range candidates {
			if err := candidate.Capabilities.Validate(); err != nil {
				return ctx, nil, err
			}

			if missing := needed.Unsatisfied(candidate.Capabilities); len(missing) == 0 {
				eligible = append(eligible, candidate.Alias)
			}
		}

		if len(eligible) == 0 {
			if model == "" {
				model = candidates[0].Model
			}

			return ctx, nil, &CapabilityError{
				Protocol:    c.Protocol(),
				Model:       model,
				Required:    needed,
				Known:       candidates[0].Capabilities,
				Unsatisfied: needed.Unsatisfied(Capabilities{}),
			}
		}

		return modelcore.WithEligibleAliases(ctx, eligible), req, nil
	}

	have, err := capabilitiesOf(c.next, ctx, req)
	if err != nil {
		return ctx, nil, &CapabilityError{
			Protocol: c.Protocol(),
			Model:    model,
			Required: needed,
			Err:      err,
		}
	}

	if err := have.Validate(); err != nil {
		return ctx, nil, err
	}

	if missing := needed.Unsatisfied(have); len(missing) > 0 {
		return ctx, nil, &CapabilityError{
			Protocol:    c.Protocol(),
			Model:       model,
			Required:    needed,
			Known:       have,
			Unsatisfied: missing,
		}
	}

	return ctx, req, nil
}

var (
	_ Caller                     = (*capabilityPolicyCaller)(nil)
	_ CapabilityProvider         = (*capabilityPolicyCaller)(nil)
	_ EndpointCapabilityProvider = (*capabilityPolicyCaller)(nil)
)
