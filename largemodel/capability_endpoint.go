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
	"maps"

	"github.com/vogo/vage/largemodel/provider/anthropics"
	"github.com/vogo/vage/largemodel/provider/openais"
)

func declaredFromOpenAI(endpoints []OpenAIEndpoint) ([]EndpointCapability, error) {
	out := make([]EndpointCapability, 0, len(endpoints))
	for _, endpoint := range endpoints {
		caps, err := cloneDeclared(endpoint.Capabilities)
		if err != nil {
			return nil, err
		}

		out = append(out, EndpointCapability{
			Alias:        endpoint.Alias,
			Model:        endpoint.Model,
			Capabilities: caps,
		})
	}

	return out, nil
}

func declaredFromAnthropic(endpoints []AnthropicEndpoint) ([]EndpointCapability, error) {
	out := make([]EndpointCapability, 0, len(endpoints))
	for _, endpoint := range endpoints {
		caps, err := cloneDeclared(endpoint.Capabilities)
		if err != nil {
			return nil, err
		}

		out = append(out, EndpointCapability{
			Alias:        endpoint.Alias,
			Model:        endpoint.Model,
			Capabilities: caps,
		})
	}

	return out, nil
}

func cloneDeclared(caps *Capabilities) (Capabilities, error) {
	if caps == nil {
		return Capabilities{}, nil
	}

	if err := caps.Validate(); err != nil {
		return Capabilities{}, err
	}

	out := *caps
	if caps.Modalities != nil {
		out.Modalities = make(map[Modality]SupportLevel, len(caps.Modalities))
		maps.Copy(out.Modalities, caps.Modalities)
	}

	return out, nil
}

func resolveDeclaredCapabilities(declared []EndpointCapability, req *Request) Capabilities {
	if len(declared) == 1 {
		return declared[0].Capabilities
	}

	if req == nil || req.Model == "" {
		return Capabilities{}
	}

	var found *EndpointCapability
	for i := range declared {
		if declared[i].Model != req.Model {
			continue
		}

		if found != nil {
			return Capabilities{}
		}

		match := declared[i]
		found = &match
	}

	if found == nil {
		return Capabilities{}
	}

	return found.Capabilities
}

func openAIProviderCapability(caps *Capabilities) *openais.Capability {
	if caps == nil {
		return nil
	}

	var out openais.Capability
	set := false

	switch caps.ToolCalling {
	case SupportNative:
		out.Tools = true
		set = true
	case SupportUnsupported:
		set = true
	}

	switch caps.modality(ModalityImage) {
	case SupportNative:
		out.Vision = true
		set = true
	case SupportUnsupported:
		set = true
	}

	if !set {
		return nil
	}

	return &out
}

func fromProviderBools(tools, vision bool) Capabilities {
	out := Capabilities{
		ToolCalling: SupportUnsupported,
		Modalities:  map[Modality]SupportLevel{ModalityImage: SupportUnsupported},
	}
	if tools {
		out.ToolCalling = SupportNative
	}

	if vision {
		out.Modalities[ModalityImage] = SupportNative
	}

	return out
}

func anthropicProviderCapability(caps *Capabilities) *anthropics.Capability {
	mapped := openAIProviderCapability(caps)
	if mapped == nil {
		return nil
	}

	return &anthropics.Capability{Tools: mapped.Tools, Vision: mapped.Vision}
}
