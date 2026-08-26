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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/router"
)

func toolsRequest() *openai.ChatCompletionRequest {
	request := testRequest()
	request.Tools = []openai.ChatCompletionTool{
		{Type: "function", Function: openai.ChatCompletionFunction{Name: "get_weather"}},
	}

	return request
}

func visionRequest() *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model: "placeholder",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: openai.NewPartsContent(
				openai.ChatCompletionContentPart{Type: "text", Text: "describe"},
				openai.ChatCompletionContentPart{Type: "image_url", ImageURL: &openai.ImageURL{URL: "https://x/y.png"}},
			)},
		},
	}
}

// newToolsCapturingServer records whether the incoming request carried tools.
func newToolsCapturingServer(t *testing.T, gotTools *atomic.Bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)

		if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
			gotTools.Store(true)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "id", Model: "m",
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{Role: "assistant", Content: openai.NewTextContent("ok")},
			}},
		})
	}))
}

func TestCapability_ToolsRoutesToTheCapableEndpoint(t *testing.T) {
	var gotTools atomic.Bool

	sTools := newToolsCapturingServer(t, &gotTools)
	defer sTools.Close()

	sPlain := newTestServer(t)
	defer sPlain.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "plain", Capability: &Capability{Tools: false}, Client: newClientForServer(t, sPlain)},
		{Name: "m1", Alias: "tools", Capability: &Capability{Tools: true}, Client: newClientForServer(t, sTools)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A plain request first, so the pool settles on the declaration-order entry.
	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m0" {
		t.Fatalf("plain request routed to %q, want m0", resp.Model)
	}

	// The tools request cannot be served by that endpoint, so it is routed to the
	// capable one for this call alone. That server is the only one recording the
	// tools, which is what proves where the request landed.
	if _, err := cc.ChatCompletions(context.Background(), toolsRequest()); err != nil {
		t.Fatal(err)
	}

	// It received the tools untouched — no stripping, no downgrade.
	if !gotTools.Load() {
		t.Fatal("tools were stripped before reaching the capable endpoint")
	}

	// Routing around the active endpoint does not move it: plain requests are
	// still served by m0.
	resp, err = cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m0" {
		t.Fatalf("plain request after the tools call routed to %q, want m0", resp.Model)
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

	_, err = cc.ChatCompletions(context.Background(), toolsRequest())

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

func TestCapability_VisionRequirement(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "text-only", Capability: &Capability{Vision: false}, Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.ChatCompletions(context.Background(), visionRequest())

	var capErr *router.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *router.CapabilityError, got %v", err)
	}

	if len(capErr.Required) != 1 || capErr.Required[0] != CapabilityVision {
		t.Fatalf("required = %v, want [%s]", capErr.Required, CapabilityVision)
	}

	if !strings.Contains(capErr.Error(), "vision") || !strings.Contains(capErr.Error(), "text-only") {
		t.Fatalf("error string missing detail: %q", capErr.Error())
	}
}

func TestCapability_ToolChoiceNoneDoesNotRequireTools(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "no-tools", Capability: &Capability{Tools: false}, Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := testRequest()
	request.ToolChoice = "none"

	if _, err := cc.ChatCompletions(context.Background(), request); err != nil {
		t.Fatalf("tool_choice none needs no tools capability, got %v", err)
	}
}

// Entries that declare no Capability are unknown, not incapable: a tools
// request must still reach them over the wire.
func TestCapability_UndeclaredIsNeverFiltered(t *testing.T) {
	var gotTools atomic.Bool

	s := newToolsCapturingServer(t, &gotTools)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "silent", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.ChatCompletions(context.Background(), toolsRequest()); err != nil {
		t.Fatalf("an undeclared endpoint must serve a tools request, got %v", err)
	}

	if !gotTools.Load() {
		t.Fatal("the tools request never reached the undeclared endpoint")
	}
}

// declaringClient exercises the CapabilityProvider fallback for clients that
// expose their own capability.
type declaringClient struct {
	chatOnlyClient

	declared Capability
}

func (d declaringClient) ComposeCapability() Capability { return d.declared }

func TestCapability_ClientDeclaration(t *testing.T) {
	entries := []ModelEntry{
		{Name: "m0", Alias: "declared", Client: declaringClient{declared: Capability{Tools: true}}},
		{Name: "m1", Alias: "silent", Client: declaringClient{}},
		{Name: "m2", Alias: "override", Client: declaringClient{}, Capability: &Capability{Tools: true}},
	}

	// A client declaration and an explicit entry declaration both produce a
	// label set; the entry's wins where both exist.
	assertLabels(t, declaresOf(&entries[0]), []string{CapabilityTools})
	assertLabels(t, declaresOf(&entries[1]), []string{})
	assertLabels(t, declaresOf(&entries[2]), []string{CapabilityTools})

	// No declaration at all stays nil — unknown, never filtered.
	undeclared := ModelEntry{Name: "m3", Client: &openai.Client{}}
	if got := declaresOf(&undeclared); got != nil {
		t.Fatalf("declaresOf for an undeclared entry = %v, want nil", got)
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
