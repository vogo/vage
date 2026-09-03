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
	"testing"

	"github.com/vogo/vage/schema"
)

func TestSupportLevel_Meets(t *testing.T) {
	tests := []struct {
		have SupportLevel
		need SupportLevel
		want bool
	}{
		{SupportNative, SupportNative, true},
		{SupportNative, SupportPrompt, true},
		{SupportPrompt, SupportNative, false},
		{SupportPrompt, SupportPrompt, true},
		{SupportUnknown, SupportNative, false},
		{SupportUnsupported, SupportNative, false},
		{SupportUnknown, SupportPrompt, false},
		{SupportNative, SupportUnknown, true},
		{SupportUnknown, SupportUnknown, true},
	}
	for _, tt := range tests {
		if got := tt.have.Meets(tt.need); got != tt.want {
			t.Errorf("%s.Meets(%s) = %v, want %v", tt.have.String(), tt.need.String(), got, tt.want)
		}
	}
}

func TestCapabilities_Validate_PromptOnlyOnStructuredOutput(t *testing.T) {
	if err := (Capabilities{ToolCalling: SupportPrompt}).Validate(); err == nil {
		t.Fatal("prompt tool calling must be rejected")
	}

	if err := (Capabilities{Modalities: map[Modality]SupportLevel{ModalityImage: SupportPrompt}}).Validate(); err == nil {
		t.Fatal("prompt image modality must be rejected")
	}

	if err := (Capabilities{StructuredOutput: SupportPrompt}).Validate(); err != nil {
		t.Fatalf("prompt structured output must be allowed: %v", err)
	}

	if err := (Capabilities{Modalities: map[Modality]SupportLevel{"audio": SupportNative}}).Validate(); err == nil {
		t.Fatal("unknown modality must be rejected")
	}
}

func TestDeriveRequirements(t *testing.T) {
	img, err := schema.ImageFromURL("https://example.com/a.png")
	if err != nil {
		t.Fatal(err)
	}

	file, err := schema.FileFromID("file-1")
	if err != nil {
		t.Fatal(err)
	}

	req := &Request{
		Messages: []schema.Message{
			schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
				{Type: schema.MessagePartText, Text: "hi"},
				img,
				file,
			}),
		},
		Tools:          []schema.ToolDef{{Name: "lookup"}},
		ResponseSchema: map[string]any{"type": "object"},
	}

	got := DeriveRequirements(req)
	if got.StructuredOutput != SupportNative || got.ToolCalling != SupportNative {
		t.Fatalf("got %+v", got)
	}

	if got.modality(ModalityImage) != SupportNative || got.modality(ModalityFile) != SupportNative {
		t.Fatalf("modalities %+v", got.Modalities)
	}

	none := DeriveRequirements(&Request{ToolChoice: ToolChoiceNoneValue()})
	if none.ToolCalling != SupportUnknown {
		t.Fatalf("none tool_choice must not require tools, got %s", none.ToolCalling)
	}

	named := DeriveRequirements(&Request{ToolChoice: ToolChoiceNamedValue("lookup")})
	if named.ToolCalling != SupportNative {
		t.Fatalf("named tool_choice requires tools")
	}
}

func TestRequirements_MergeDoesNotWeaken(t *testing.T) {
	implied := Requirements{StructuredOutput: SupportNative, ToolCalling: SupportNative}
	extra := Requirements{StructuredOutput: SupportPrompt, Modalities: map[Modality]SupportLevel{ModalityImage: SupportNative}}
	got := implied.Merge(extra)
	if got.StructuredOutput != SupportNative {
		t.Fatalf("merge weakened structured output to %s", got.StructuredOutput)
	}

	if got.ToolCalling != SupportNative || got.modality(ModalityImage) != SupportNative {
		t.Fatalf("got %+v", got)
	}
}

func TestRequirements_Unsatisfied(t *testing.T) {
	need := Requirements{StructuredOutput: SupportNative, ToolCalling: SupportNative}
	have := Capabilities{StructuredOutput: SupportNative}
	got := need.Unsatisfied(have)
	if len(got) != 1 || got[0] != "tool_calling" {
		t.Fatalf("unsatisfied = %v", got)
	}
}

func TestOpenAIProviderCapability_UnknownStaysNil(t *testing.T) {
	if got := openAIProviderCapability(nil); got != nil {
		t.Fatalf("nil declaration must stay nil, got %+v", got)
	}

	unknown := Capabilities{StructuredOutput: SupportNative}
	if got := openAIProviderCapability(&unknown); got != nil {
		t.Fatalf("undeclared tools/vision must stay nil, got %+v", got)
	}

	native := Capabilities{ToolCalling: SupportNative, Modalities: map[Modality]SupportLevel{ModalityImage: SupportNative}}
	got := openAIProviderCapability(&native)
	if got == nil || !got.Tools || !got.Vision {
		t.Fatalf("native tools+vision = %+v", got)
	}

	legacy := fromProviderBools(true, false)
	if legacy.ToolCalling != SupportNative || legacy.modality(ModalityImage) != SupportUnsupported {
		t.Fatalf("legacy mapping = %+v", legacy)
	}
}

func TestNewCaller_ExposesDeclaredCapabilities(t *testing.T) {
	caller, err := NewCaller(OpenAIEndpoint{
		APIKey: "sk-test",
		Model:  "gpt-4o",
		Capabilities: &Capabilities{
			StructuredOutput: SupportNative,
			ToolCalling:      SupportNative,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	provider, ok := caller.(CapabilityProvider)
	if !ok {
		t.Fatal("compose caller must implement CapabilityProvider")
	}

	got, err := provider.Capabilities(context.Background(), &Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}

	if got.StructuredOutput != SupportNative || got.ToolCalling != SupportNative {
		t.Fatalf("got %+v", got)
	}

	endpoints, ok := caller.(EndpointCapabilityProvider)
	if !ok || len(endpoints.EndpointCapabilities()) != 1 {
		t.Fatal("compose caller must expose endpoint declarations")
	}
}
