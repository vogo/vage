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
	"errors"
	"io"
	"testing"

	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

func TestRequireNativeCapabilities_UnknownFailsBeforeBackend(t *testing.T) {
	fake := &FakeCaller{Responses: []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, "ok", schema.Usage{})}}
	caller := RequireNativeCapabilities(fake)
	req := &Request{
		Model:          "m",
		Messages:       []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		ResponseSchema: map[string]any{"type": "object"},
	}

	_, err := caller.Call(context.Background(), req)
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("Call err = %v", err)
	}

	if _, ok := errors.AsType[*CapabilityError](err); !ok {
		t.Fatalf("want *CapabilityError, got %T", err)
	}

	if fake.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0", fake.Calls())
	}

	_, streamErr := caller.CallStream(context.Background(), req)
	if !errors.Is(streamErr, ErrCapabilityUnavailable) {
		t.Fatalf("CallStream err = %v", streamErr)
	}
}

func TestRequireNativeCapabilities_UnsupportedFailsBeforeBackend(t *testing.T) {
	fake := &FakeCaller{
		Responses:   []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, "ok", schema.Usage{})},
		DeclaredSet: true,
		Declared:    Capabilities{StructuredOutput: SupportUnsupported},
	}
	_, err := RequireNativeCapabilities(fake).Call(context.Background(), &Request{
		ResponseSchema: map[string]any{"type": "string"},
	})
	if !errors.Is(err, ErrCapabilityUnavailable) || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestRequireNativeCapabilities_QueryErrorFailsBeforeBackend(t *testing.T) {
	queryErr := errors.New("catalog down")
	fake := &FakeCaller{
		Responses:   []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, "ok", schema.Usage{})},
		DeclaredErr: queryErr,
	}
	_, err := RequireNativeCapabilities(fake).Call(context.Background(), &Request{
		ResponseSchema: map[string]any{"type": "string"},
	})
	if !errors.Is(err, ErrCapabilityUnavailable) || !errors.Is(err, queryErr) || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestRequireNativeCapabilities_SatisfiedReachesBackend(t *testing.T) {
	fake := &FakeCaller{
		Responses:   []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, `{"ok":true}`, schema.Usage{})},
		DeclaredSet: true,
		Declared: Capabilities{
			StructuredOutput: SupportNative,
			ToolCalling:      SupportNative,
			Modalities:       map[Modality]SupportLevel{ModalityImage: SupportNative},
		},
	}
	img, err := schema.ImageFromURL("https://example.com/a.png")
	if err != nil {
		t.Fatal(err)
	}

	_, err = RequireNativeCapabilities(fake).Call(context.Background(), &Request{
		Messages: []schema.Message{schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
			{Type: schema.MessagePartText, Text: "hi"},
			img,
		})},
		Tools:          []schema.ToolDef{{Name: "lookup"}},
		ResponseSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if fake.Calls() != 1 {
		t.Fatalf("calls = %d", fake.Calls())
	}
}

func TestRequireNativeCapabilities_CombinationNotUnioned(t *testing.T) {
	fake := &FakeCaller{
		Responses: []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, "ok", schema.Usage{})},
		Endpoints: []EndpointCapability{
			{Alias: "vision", Capabilities: Capabilities{Modalities: map[Modality]SupportLevel{ModalityImage: SupportNative}}},
			{Alias: "tools", Capabilities: Capabilities{ToolCalling: SupportNative}},
		},
	}
	img, err := schema.ImageFromURL("https://example.com/a.png")
	if err != nil {
		t.Fatal(err)
	}

	_, err = RequireNativeCapabilities(fake).Call(context.Background(), &Request{
		Messages: []schema.Message{schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{img})},
		Tools:    []schema.ToolDef{{Name: "lookup"}},
	})
	if !errors.Is(err, ErrCapabilityUnavailable) || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestAllowPromptFallback_ConflictsWithExplicitNative(t *testing.T) {
	fake := &FakeCaller{DeclaredSet: true, Declared: Capabilities{StructuredOutput: SupportNative}}
	caller := AllowPromptFallback(RequireNativeCapabilities(fake, Requirements{StructuredOutput: SupportNative}))
	_, err := caller.Call(context.Background(), &Request{ResponseSchema: map[string]any{"type": "object"}})
	if err == nil {
		t.Fatal("expected config conflict")
	}
}

func TestAllowPromptFallback_PromptLevelAccepted(t *testing.T) {
	fake := &FakeCaller{
		Responses:   []*Response{FakeStopResponse(schema.ProtocolOpenAIChat, `{"a":1}`, schema.Usage{})},
		DeclaredSet: true,
		Declared:    Capabilities{StructuredOutput: SupportPrompt},
	}
	_, err := AllowPromptFallback(RequireNativeCapabilities(fake)).Call(context.Background(), &Request{
		ResponseSchema: map[string]any{"type": "object"},
	})
	if err != nil || fake.Calls() != 1 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestDefaultNoNativeMapping_ErrorsWithoutFallback(t *testing.T) {
	stub := &countingCodec{proto: syntheticProtocol}
	caller := &codecCaller{codec: stub}
	_, err := caller.Call(context.Background(), &Request{
		Model:          "m",
		Messages:       []schema.Message{schema.NewUserMessage(syntheticProtocol, "hi")},
		ResponseSchema: map[string]any{"type": "string"},
	})
	if !errors.Is(err, ErrCapabilityUnavailable) || stub.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, stub.calls)
	}
}

func TestDefaultNoNativeMapping_PromptFallbackInjects(t *testing.T) {
	stub := &countingCodec{proto: syntheticProtocol}
	caller := AllowPromptFallback(&codecCaller{codec: stub})
	_, err := caller.Call(context.Background(), &Request{
		Model:          "m",
		Messages:       []schema.Message{schema.NewUserMessage(syntheticProtocol, "hi")},
		ResponseSchema: map[string]any{"type": "string"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if stub.calls != 1 {
		t.Fatalf("calls = %d", stub.calls)
	}

	got := stub.lastReq
	if got.ResponseSchema != nil {
		t.Fatal("degraded request must clear ResponseSchema")
	}

	if len(got.Messages) != 2 || got.Messages[0].Role() != schema.RoleSystem {
		t.Fatalf("messages = %+v", got.Messages)
	}
}

func TestModelForwardsCapabilities(t *testing.T) {
	fake := &FakeCaller{
		DeclaredSet: true,
		Declared:    Capabilities{ToolCalling: SupportNative},
	}
	model := New(fake)
	got, err := model.Capabilities(context.Background(), &Request{})
	if err != nil {
		t.Fatal(err)
	}

	if got.ToolCalling != SupportNative {
		t.Fatalf("got %+v", got)
	}
}

func TestRequest_Clone_PreservesFormalFieldsAndExtensions(t *testing.T) {
	topP := 0.2
	seed := int64(7)
	freq := 0.1
	pres := 0.3
	req := &Request{
		TopP:             &topP,
		Seed:             &seed,
		FrequencyPenalty: &freq,
		PresencePenalty:  &pres,
		ToolChoice:       ToolChoiceNamedValue("lookup"),
		ProviderExtensions: map[string]any{
			"openais": map[string]any{"enable_thinking": true},
		},
	}
	clone := req.Clone()
	if clone.TopP == nil || *clone.TopP != topP || clone.Seed == nil || *clone.Seed != seed {
		t.Fatalf("clone sampling = %+v", clone)
	}

	if clone.ToolChoice == nil || clone.ToolChoice.Name != "lookup" {
		t.Fatalf("clone tool_choice = %+v", clone.ToolChoice)
	}

	clone.ToolChoice.Name = "other"
	if req.ToolChoice.Name != "lookup" {
		t.Fatal("clone must not alias ToolChoice")
	}

	ext, _ := clone.ProviderExtensions["openais"].(map[string]any)
	ext["enable_thinking"] = false
	orig, _ := req.ProviderExtensions["openais"].(map[string]any)
	if orig["enable_thinking"] != true {
		t.Fatal("clone must copy extension map")
	}
}

func TestCall_NamedToolChoiceUnknownFailsBeforeCall(t *testing.T) {
	codec := &countingCodec{proto: schema.ProtocolOpenAIChat, native: true}
	_, err := (&codecCaller{codec: codec}).Call(context.Background(), &Request{
		Tools:      []schema.ToolDef{{Name: "lookup"}},
		ToolChoice: ToolChoiceNamedValue("missing"),
	})
	if err == nil || codec.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, codec.calls)
	}
}

type countingCodec struct {
	proto   schema.Protocol
	native  bool
	calls   int
	lastReq *modelcore.Request
}

func (c *countingCodec) Protocol() schema.Protocol { return c.proto }

func (c *countingCodec) NativeStructuredOutput() bool { return c.native }

func (c *countingCodec) Call(_ context.Context, req *modelcore.Request) (*modelcore.Result, error) {
	c.calls++
	c.lastReq = req

	return &modelcore.Result{Message: schema.NewAssistantTurn(c.proto, "{}", "", nil)}, nil
}

func (c *countingCodec) CallStream(context.Context, *modelcore.Request) (*modelcore.Stream, error) {
	return &modelcore.Stream{Recv: func() (*modelcore.Chunk, error) { return nil, io.EOF }, Close: func() error { return nil }}, nil
}
