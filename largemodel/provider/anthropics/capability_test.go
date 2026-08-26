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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/router"
)

func toolsRequest() *anthropic.MessagesRequest {
	request := testRequest()
	request.Tools = []anthropic.MessagesTool{{Name: "get_weather", Description: "weather"}}

	return request
}

func visionRequest(t *testing.T) *anthropic.MessagesRequest {
	t.Helper()

	request := testRequest()
	request.Messages = []anthropic.MessagesMessage{blocksMessage(
		t,
		anthropic.ContentBlock{Type: anthropic.ContentBlockTypeText, Text: "describe"},
		anthropic.ContentBlock{
			Type: anthropic.ContentBlockTypeImage,
			Source: &anthropic.ContentSource{
				Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo=",
			},
		},
	)}

	return request
}

func TestCapability_ToolsRoutesToTheCapableEndpoint(t *testing.T) {
	var gotTools atomic.Bool

	sTools := newTestServer(t, &gotTools)
	defer sTools.Close()

	sPlain := newTestServer(t, nil)
	defer sPlain.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "plain", Capability: &Capability{Tools: false}, Client: newClientForServer(t, sPlain)},
		{Name: "m1", Alias: "tools", Capability: &Capability{Tools: true}, Client: newClientForServer(t, sTools)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A plain request first, so the pool settles on the declaration-order entry.
	plain, err := cc.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if plain.Model != "m0" {
		t.Fatalf("plain request routed to %q, want m0", plain.Model)
	}

	// The tools request cannot be served there, so it is routed to the capable
	// endpoint for this call alone.
	resp, err := cc.Messages(context.Background(), toolsRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want the tools-capable m1", resp.Model)
	}

	// The tools reached it untouched — no stripping or downgrade.
	if !gotTools.Load() {
		t.Fatal("tools were stripped before reaching the capable endpoint")
	}

	// Routing around the active endpoint does not move it.
	plain, err = cc.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if plain.Model != "m0" {
		t.Fatalf("plain request after the tools call routed to %q, want m0", plain.Model)
	}

	for _, stat := range cc.Stats() {
		if (stat.Alias == "plain") != stat.Active {
			t.Fatalf("a temporary pick moved the active endpoint: %+v", stat)
		}
	}
}

func TestCapability_AllIncapableErrorsBeforeNetwork(t *testing.T) {
	var hits atomic.Int64

	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "a", Capability: &Capability{Tools: false}, Client: newClientForServer(t, s)},
		{Name: "m1", Alias: "b", Capability: &Capability{Tools: false}, Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.Messages(context.Background(), toolsRequest())

	if !errors.Is(err, router.ErrCapabilityNotSatisfied) {
		t.Fatalf("expected router.ErrCapabilityNotSatisfied, got %v", err)
	}

	var capErr *router.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *router.CapabilityError, got %T", err)
	}

	if len(capErr.Required) != 1 || capErr.Required[0] != CapabilityTools {
		t.Fatalf("required = %v, want [%s]", capErr.Required, CapabilityTools)
	}

	if hits.Load() != 0 {
		t.Fatalf("expected zero network calls, got %d", hits.Load())
	}
}

// Vision comes from Anthropic's own content blocks: an image block in the
// array form of `content`.
func TestCapability_VisionFromImageContentBlock(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "text-only", Capability: &Capability{Vision: false}, Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.Messages(context.Background(), visionRequest(t))

	var capErr *router.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *router.CapabilityError, got %v", err)
	}

	if len(capErr.Required) != 1 || capErr.Required[0] != CapabilityVision {
		t.Fatalf("required = %v, want [%s]", capErr.Required, CapabilityVision)
	}

	if !strings.Contains(capErr.Error(), "text-only") {
		t.Fatalf("error string missing the considered alias: %q", capErr.Error())
	}
}

// Requirements are read from Anthropic's own fields, in Anthropic's own shapes:
// a typed tool_choice object, and raw-JSON message content.
func TestRequirements_ReadAnthropicFieldsOnly(t *testing.T) {
	if got := messagesRequires(testRequest()); len(got) != 0 {
		t.Fatalf("a plain request requires %v, want nothing", got)
	}

	if got := messagesRequires(toolsRequest()); len(got) != 1 || got[0] != CapabilityTools {
		t.Fatalf("a tools request requires %v, want [%s]", got, CapabilityTools)
	}

	// tool_choice is a typed object here, not a polymorphic value.
	none := testRequest()
	none.ToolChoice = &anthropic.ToolChoice{Type: anthropic.ToolChoiceTypeNone}

	if got := messagesRequires(none); len(got) != 0 {
		t.Fatalf("tool_choice none requires %v, want nothing", got)
	}

	auto := testRequest()
	auto.ToolChoice = &anthropic.ToolChoice{Type: anthropic.ToolChoiceTypeAuto}

	if got := messagesRequires(auto); len(got) != 1 || got[0] != CapabilityTools {
		t.Fatalf("tool_choice auto requires %v, want [%s]", got, CapabilityTools)
	}

	if got := messagesRequires(visionRequest(t)); len(got) != 1 || got[0] != CapabilityVision {
		t.Fatalf("an image request requires %v, want [%s]", got, CapabilityVision)
	}

	// The scalar string form of content carries no blocks and needs nothing.
	scalar := testRequest()
	scalar.Messages = []anthropic.MessagesMessage{{Role: "user", Content: json.RawMessage(`"plain text"`)}}

	if got := messagesRequires(scalar); len(got) != 0 {
		t.Fatalf("scalar content requires %v, want nothing", got)
	}

	// Content this wrapper cannot decode never blocks a request the backend
	// might well accept.
	broken := testRequest()
	broken.Messages = []anthropic.MessagesMessage{{Role: "user", Content: json.RawMessage(`[{"type":`)}}

	if got := messagesRequires(broken); len(got) != 0 {
		t.Fatalf("undecodable content requires %v, want nothing", got)
	}

	// A text-only block array needs nothing either.
	textBlocks := testRequest()
	textBlocks.Messages = []anthropic.MessagesMessage{
		blocksMessage(t, anthropic.ContentBlock{Type: anthropic.ContentBlockTypeText, Text: "hi"}),
	}

	if got := messagesRequires(textBlocks); len(got) != 0 {
		t.Fatalf("text blocks require %v, want nothing", got)
	}
}

// Entries that declare no Capability are unknown, not incapable.
func TestCapability_UndeclaredIsNeverFiltered(t *testing.T) {
	var gotTools atomic.Bool

	s := newTestServer(t, &gotTools)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "silent", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.Messages(context.Background(), toolsRequest()); err != nil {
		t.Fatalf("an undeclared endpoint must serve a tools request, got %v", err)
	}

	if !gotTools.Load() {
		t.Fatal("the tools request never reached the undeclared endpoint")
	}
}

// declaringClient exercises the CapabilityProvider fallback.
type declaringClient struct {
	*anthropic.Client

	declared Capability
}

func (d declaringClient) ComposeCapability() Capability { return d.declared }

func TestCapability_ClientDeclaration(t *testing.T) {
	client := anthropic.NewClient("k")

	entries := []ModelEntry{
		{Name: "m0", Client: declaringClient{Client: client, declared: Capability{Tools: true}}},
		{Name: "m1", Client: declaringClient{Client: client}},
		{Name: "m2", Client: declaringClient{Client: client}, Capability: &Capability{Vision: true}},
		{Name: "m3", Client: client},
	}

	assertLabels(t, declaresOf(&entries[0]), []string{CapabilityTools})
	assertLabels(t, declaresOf(&entries[1]), []string{})
	assertLabels(t, declaresOf(&entries[2]), []string{CapabilityVision})

	// No declaration at all stays nil — unknown, never filtered.
	if got := declaresOf(&entries[3]); got != nil {
		t.Fatalf("declaresOf for an undeclared entry = %v, want nil", got)
	}
}

// Cost ordering reads Anthropic's own required max_tokens as the output volume.
func TestOutputUnits_ComeFromMaxTokens(t *testing.T) {
	request := testRequest()
	if got := messagesOutputUnits(request); got != 64 {
		t.Fatalf("messagesOutputUnits = %v, want 64", got)
	}

	request.MaxTokens = 0

	if got := messagesOutputUnits(request); got != 1 {
		t.Fatalf("messagesOutputUnits without a cap = %v, want 1", got)
	}
}

func assertLabels(t *testing.T, got, want []string) {
	t.Helper()

	if got == nil {
		t.Fatalf("labels = nil, want a declaration of %v", want)
	}

	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}

	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("labels = %v, want %v", got, want)
		}
	}
}
