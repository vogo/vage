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
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/aimodel/openai"
)

// newResponsesServer echoes the requested model back in a valid Responses
// object, and records whether the request carried tools.
func newResponsesServer(t *testing.T, gotTools *atomic.Bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if gotTools != nil {
			if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
				gotTools.Store(true)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_1",
			"status": "completed",
			"model":  request["model"],
			"output": []any{map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}},
			}},
		})
	}))
}

// newResponsesStreamServer emits a single response.completed SSE event.
func newResponsesStreamServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)

		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)

			return
		}

		payload, _ := json.Marshal(map[string]any{
			"type":            "response.completed",
			"sequence_number": 1,
			"response":        map[string]any{"id": "resp_1", "status": "completed", "model": request["model"]},
		})

		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
		flusher.Flush()
	}))
}

func responsesRequest() *openai.ResponsesRequest {
	return &openai.ResponsesRequest{
		Model: "placeholder",
		Input: openai.NewResponseTextInput("hi"),
	}
}

// chatOnlyClient implements ChatCompleter and nothing else, which is what keeps
// it out of Responses routing.
type chatOnlyClient struct{}

func (chatOnlyClient) ChatCompletions(
	context.Context, *openai.ChatCompletionRequest,
) (*openai.ChatCompletionResponse, error) {
	return &openai.ChatCompletionResponse{Model: "chat-only"}, nil
}

func (chatOnlyClient) ChatCompletionsStream(
	context.Context, *openai.ChatCompletionRequest,
) (*openai.ChatCompletionStream, error) {
	return nil, errors.New("not supported")
}

func TestResponses_RoutesAndOverridesTheModel(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "gpt-5", Alias: "primary", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := responsesRequest()

	resp, err := cc.Responses(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "gpt-5" {
		t.Fatalf("model = %q, want the endpoint's own gpt-5", resp.Model)
	}

	// The caller's request is untouched: the override happens on a copy.
	if request.Model != "placeholder" {
		t.Fatalf("the caller's request was mutated to %q", request.Model)
	}
}

func TestResponses_FailsOverAndAttributesEveryAlias(t *testing.T) {
	sFail, sOK := newFailServer(t), newResponsesServer(t, nil)
	defer sFail.Close()
	defer sOK.Close()

	obs := &recordingObserver{}

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "broken", Client: newClientForServer(t, sFail)},
		{Name: "m1", Alias: "healthy", Client: newClientForServer(t, sOK)},
	}, router.WithAttemptObserver(obs.fn))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want the failover m1", resp.Model)
	}

	results := obs.seen()
	if len(results) != 2 || results[0].Alias != "broken" || results[0].Success || !results[1].Success {
		t.Fatalf("observations = %+v, want broken/failure then healthy/success", results)
	}

	// The failure marked the shared health state, visible to every form.
	if s := cc.Stats()[0]; s.Status != router.StatusDead {
		t.Fatalf("broken endpoint status = %q, want dead", s.Status)
	}
}

func TestResponses_AllFailAggregatesByAlias(t *testing.T) {
	s0, s1 := newFailServer(t), newFailServer(t)
	defer s0.Close()
	defer s1.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "a", Client: newClientForServer(t, s0)},
		{Name: "m1", Alias: "b", Client: newClientForServer(t, s1)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.Responses(context.Background(), responsesRequest())

	var multi *router.MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("expected *router.MultiError, got %T: %v", err, err)
	}

	if len(multi.Errors) != 2 || multi.Errors[0].Alias != "a" || multi.Errors[1].Alias != "b" {
		t.Fatalf("aliases = %+v, want a then b", multi.Errors)
	}
}

func TestResponsesStream_FailsOverOnEstablishment(t *testing.T) {
	sFail, sStream := newFailServer(t), newResponsesStreamServer(t)
	defer sFail.Close()
	defer sStream.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "broken", Client: newClientForServer(t, sFail)},
		{Name: "m1", Alias: "streamer", Client: newClientForServer(t, sStream)},
	})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := cc.ResponsesStream(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stream.Close() }()

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}

	if event.Response == nil || event.Response.Model != "m1" {
		t.Fatalf("event = %+v, want a completed response from m1", event)
	}
}

// Responses runs through the same core loop as chat, so every strategy fails
// over identically.
func TestResponses_EveryStrategyFailsOverToTheHealthyEndpoint(t *testing.T) {
	strategies := []router.Strategy{
		router.StrategyFailover,
		router.StrategyRandom,
		router.StrategyWeight,
		router.StrategyCost,
		router.StrategyLatency,
	}

	for _, strategy := range strategies {
		t.Run(string(strategy), func(t *testing.T) {
			sFail, sOK := newFailServer(t), newResponsesServer(t, nil)
			defer sFail.Close()
			defer sOK.Close()

			fast, slow := 10*time.Millisecond, 50*time.Millisecond

			cc, err := newRoutingClient(t, strategy, []ModelEntry{
				{
					Name: "m0", Alias: "broken", Weight: 9, Client: newClientForServer(t, sFail),
					Cost: &router.EndpointCost{InputPrice: 1, OutputPrice: 1}, Latency: &fast,
				},
				{
					Name: "m1", Alias: "healthy", Weight: 1, Client: newClientForServer(t, sOK),
					Cost: &router.EndpointCost{InputPrice: 5, OutputPrice: 5}, Latency: &slow,
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			resp, err := cc.Responses(context.Background(), responsesRequest())
			if err != nil {
				t.Fatalf("%s: expected failover to succeed, got %v", strategy, err)
			}

			if resp.Model != "m1" {
				t.Fatalf("%s: model = %q, want m1", strategy, resp.Model)
			}
		})
	}
}

// The pool's active endpoint holds across Responses calls too.
func TestResponses_SuccessiveCallsStayOnOneBackend(t *testing.T) {
	entries := make([]ModelEntry, 3)

	for i, alias := range []string{"a", "b", "c"} {
		s := newResponsesServer(t, nil)
		t.Cleanup(s.Close)

		entries[i] = ModelEntry{Name: "m" + alias, Alias: alias, Client: newClientForServer(t, s)}
	}

	cc, err := newRoutingClient(t, router.StrategyRandom, entries)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	first, err := cc.Responses(ctx, responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	for range 10 {
		resp, err := cc.Responses(ctx, responsesRequest())
		if err != nil {
			t.Fatal(err)
		}

		if resp.Model != first.Model {
			t.Fatalf("the active endpoint drifted: %q then %q", first.Model, resp.Model)
		}
	}
}

// Health is one pool state, whichever interaction form observes it: a chat
// failure kills the endpoint and the next Responses call routes around it.
func TestResponses_HealthIsSharedWithChat(t *testing.T) {
	var hits atomic.Int64

	sFlaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "boom"}})

			return
		}

		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1", "status": "completed", "model": request["model"],
		})
	}))
	defer sFlaky.Close()

	sOK := newResponsesServer(t, nil)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "flaky", Client: newClientForServer(t, sFlaky)},
		{Name: "m1", Alias: "steady", Client: newClientForServer(t, sOK)},
	}, router.WithRecoverTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// A chat failure kills the endpoint and moves the pool to the other one...
	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead {
		t.Fatalf("status after the chat 5xx = %q, want dead", s.Status)
	}

	// ...and the Responses call sees that same state, so it never contacts it.
	resp, err := cc.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want m1 — the dead endpoint must stay out", resp.Model)
	}

	if hits.Load() != 1 {
		t.Fatalf("the dead endpoint was contacted %d times, want 1", hits.Load())
	}
}

// An entry whose client cannot serve Responses is skipped rather than
// attempted; chat still routes to it normally.
func TestResponses_SkipsEntriesWithoutTheMethodSet(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "chat-only", Alias: "chat-only", Client: chatOnlyClient{}},
		{Name: "full", Alias: "full", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "full" {
		t.Fatalf("model = %q, want full (the chat-only entry cannot serve Responses)", resp.Model)
	}

	// The Responses call selected the pool's active endpoint, because it had
	// none: a capability-restricted call may choose the first active, it just may
	// not displace one. Chat then follows that choice.
	chatResp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if chatResp.Model != "full" {
		t.Fatalf("chat model = %q, want full — the pool has an active endpoint now", chatResp.Model)
	}
}

// Once the pool has settled on a chat-only endpoint, a Responses call is routed
// around it without moving it.
func TestResponses_RoutesAroundAChatOnlyActiveEndpoint(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "chat-only", Alias: "chat-only", Client: chatOnlyClient{}},
		{Name: "full", Alias: "full", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "full" {
		t.Fatalf("model = %q, want full", resp.Model)
	}

	// The Responses call was a temporary pick: chat is still on chat-only.
	chatResp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if chatResp.Model != "chat-only" {
		t.Fatalf("chat model = %q, want the incumbent chat-only", chatResp.Model)
	}

	for _, stat := range cc.Stats() {
		if stat.Alias == "chat-only" && !stat.Active {
			t.Fatalf("chat-only should still be the active endpoint: %+v", stat)
		}

		if stat.Alias == "full" && stat.Active {
			t.Fatalf("a temporary pick became the active endpoint: %+v", stat)
		}
	}
}

// With no entry able to serve Responses, the call fails before any network I/O.
func TestResponses_NoCapableEndpointFailsFast(t *testing.T) {
	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "chat-only", Alias: "only", Client: chatOnlyClient{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.Responses(context.Background(), responsesRequest())

	if !errors.Is(err, router.ErrCapabilityNotSatisfied) {
		t.Fatalf("expected router.ErrCapabilityNotSatisfied, got %v", err)
	}

	var capErr *router.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *router.CapabilityError, got %T", err)
	}

	if len(capErr.Required) != 1 || capErr.Required[0] != CapabilityResponses {
		t.Fatalf("required = %v, want [%s]", capErr.Required, CapabilityResponses)
	}

	if len(capErr.Considered) != 1 || capErr.Considered[0] != "only" {
		t.Fatalf("considered = %v, want [only]", capErr.Considered)
	}

	// The streaming form fails the same way.
	if _, err := cc.ResponsesStream(context.Background(), responsesRequest()); !errors.Is(err, router.ErrCapabilityNotSatisfied) {
		t.Fatalf("stream: expected router.ErrCapabilityNotSatisfied, got %v", err)
	}
}

// Responses capability requirements come from the Responses request's own
// fields — a tools-declaring endpoint is required only when it declares tools.
func TestResponses_CapabilityFilterUsesResponsesFields(t *testing.T) {
	var gotTools atomic.Bool

	sTools := newResponsesServer(t, &gotTools)
	defer sTools.Close()

	sPlain := newResponsesServer(t, nil)
	defer sPlain.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{
			Name: "plain", Alias: "plain", Client: newClientForServer(t, sPlain),
			Capability: &Capability{Tools: false},
		},
		{
			Name: "tools", Alias: "tools", Client: newClientForServer(t, sTools),
			Capability: &Capability{Tools: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := responsesRequest()
	request.Tools = []openai.ResponseTool{{Type: "function", Name: "get_weather"}}

	resp, err := cc.Responses(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "tools" {
		t.Fatalf("model = %q, want the tools-capable endpoint", resp.Model)
	}

	// The tools reached the endpoint untouched — no stripping or downgrade.
	if !gotTools.Load() {
		t.Fatal("tools were stripped before reaching the capable endpoint")
	}
}

func TestResponses_VisionRequirementFromInputImage(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{
			Name: "text-only", Alias: "text-only", Client: newClientForServer(t, s),
			Capability: &Capability{Vision: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := responsesRequest()
	request.Input = openai.NewResponseItemsInput(openai.ResponseInputItem{
		Type: openai.ResponseItemTypeMessage,
		Message: &openai.ResponseInputMessage{
			Type: openai.ResponseItemTypeMessage,
			Role: "user",
			Content: openai.NewResponseContentParts(
				openai.ResponseContent{Type: openai.ResponseContentTypeInputText, Text: "describe"},
				openai.ResponseContent{Type: openai.ResponseContentTypeInputImage, ImageURL: "https://x/y.png"},
			),
		},
	})

	_, err = cc.Responses(context.Background(), request)

	var capErr *router.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *router.CapabilityError, got %v", err)
	}

	if len(capErr.Required) != 1 || capErr.Required[0] != CapabilityVision {
		t.Fatalf("required = %v, want [%s]", capErr.Required, CapabilityVision)
	}
}

// The two interaction forms read their own protocol's fields. A chat request
// carrying tools says nothing about a Responses request that carries none.
func TestRequirements_AreReadPerInteractionForm(t *testing.T) {
	chat := testRequest()
	chat.Tools = []openai.ChatCompletionTool{
		{Type: "function", Function: openai.ChatCompletionFunction{Name: "get_weather"}},
	}

	if got := chatRequires(chat); len(got) != 1 || got[0] != CapabilityTools {
		t.Fatalf("chatRequires = %v, want [tools]", got)
	}

	if got := responsesRequires(responsesRequest()); len(got) != 0 {
		t.Fatalf("responsesRequires = %v, want none", got)
	}

	withTools := responsesRequest()
	withTools.Tools = []openai.ResponseTool{{Type: "web_search"}}

	if got := responsesRequires(withTools); len(got) != 1 || got[0] != CapabilityTools {
		t.Fatalf("responsesRequires with a hosted tool = %v, want [tools]", got)
	}

	// tool_choice "none" needs nothing, on either form.
	noneChat := testRequest()
	noneChat.ToolChoice = "none"

	if got := chatRequires(noneChat); len(got) != 0 {
		t.Fatalf("chatRequires with tool_choice none = %v, want none", got)
	}

	noneResponses := responsesRequest()
	noneResponses.ToolChoice = "none"

	if got := responsesRequires(noneResponses); len(got) != 0 {
		t.Fatalf("responsesRequires with tool_choice none = %v, want none", got)
	}

	// An object tool_choice does require tools.
	objectChoice := responsesRequest()
	objectChoice.ToolChoice = map[string]any{"type": "function", "name": "f"}

	if got := responsesRequires(objectChoice); len(got) != 1 || got[0] != CapabilityTools {
		t.Fatalf("responsesRequires with an object tool_choice = %v, want [tools]", got)
	}
}

// Cost ordering reads each form's own output cap, which the wrapper passes to
// the neutral core as plain units.
func TestOutputUnits_ComeFromEachFormsOwnCap(t *testing.T) {
	chat := testRequest()
	if got := chatOutputUnits(chat); got != 1 {
		t.Fatalf("chatOutputUnits without a cap = %v, want 1", got)
	}

	cap1000 := 1000
	chat.MaxCompletionTokens = &cap1000

	if got := chatOutputUnits(chat); got != 1000 {
		t.Fatalf("chatOutputUnits = %v, want 1000", got)
	}

	legacy := testRequest()
	legacy.MaxTokens = &cap1000 //nolint:staticcheck // intentionally exercising the legacy fallback

	if got := chatOutputUnits(legacy); got != 1000 {
		t.Fatalf("chatOutputUnits via the legacy cap = %v, want 1000", got)
	}

	responses := responsesRequest()
	if got := responsesOutputUnits(responses); got != 1 {
		t.Fatalf("responsesOutputUnits without a cap = %v, want 1", got)
	}

	responses.MaxOutputTokens = &cap1000

	if got := responsesOutputUnits(responses); got != 1000 {
		t.Fatalf("responsesOutputUnits = %v, want 1000", got)
	}
}
