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
	"testing"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/router"
)

func specForServer(s *httptest.Server, alias, model string) EndpointSpec {
	return EndpointSpec{BaseURL: s.URL, APIKey: "key-" + alias, Model: model, Alias: alias}
}

func TestNewFromEndpoints_Success(t *testing.T) {
	s0, s1 := newTestServer(t), newTestServer(t)
	defer s0.Close()
	defer s1.Close()

	cc, err := NewFromEndpoints(router.StrategyWeight, []EndpointSpec{
		specForServer(s0, "primary", "gpt-4o"),
		{BaseURL: s1.URL, APIKey: "k", Model: "gpt-4o", Alias: "canary", Weight: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats := cc.Stats()
	if len(stats) != 2 || stats[0].Alias != "primary" || stats[1].Alias != "canary" {
		t.Fatalf("endpoints = %+v, want primary then canary", stats)
	}

	// Same model, distinct endpoints — a request must succeed.
	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("chat error: %v", err)
	}

	if resp.Model != "gpt-4o" {
		t.Fatalf("model = %q, want gpt-4o", resp.Model)
	}
}

func TestNewFromEndpoints_Empty(t *testing.T) {
	if _, err := NewFromEndpoints(router.StrategyFailover, nil); err == nil {
		t.Fatal("expected an error for empty specs")
	}
}

func TestNewFromEndpoints_MissingAlias(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	_, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: s.URL, APIKey: "k", Model: "m"},
	})
	if err == nil || !strings.Contains(err.Error(), "alias is required") {
		t.Fatalf("expected a missing-alias error, got %v", err)
	}
}

func TestNewFromEndpoints_DuplicateAlias(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	_, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		specForServer(s, "dup", "m"),
		specForServer(s, "dup", "m"),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("expected a duplicate-alias error, got %v", err)
	}
}

// The native client performs no construction-time validation, so an empty
// credential does not fail NewFromEndpoints — it surfaces at request time.
func TestNewFromEndpoints_EmptyAPIKeyConstructs(t *testing.T) {
	cc, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: "http://localhost:1", APIKey: "", Model: "m", Alias: "nokey"},
	})
	if err != nil {
		t.Fatalf("native construction does not validate credentials: %v", err)
	}

	if stats := cc.Stats(); len(stats) != 1 || stats[0].Alias != "nokey" {
		t.Fatalf("endpoint not built: %+v", stats)
	}
}

func TestNewFromEndpoints_CredentialReachesEndpoint(t *testing.T) {
	var gotAuth string

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "id", Model: "m"})
	}))
	defer s.Close()

	cc, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: s.URL, APIKey: "secret-key", Model: "m", Alias: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotAuth, "secret-key") {
		t.Fatalf("the endpoint did not receive its explicit key, auth = %q", gotAuth)
	}
}

// A declaratively built pool serves Responses too: the clients it constructs
// implement both method sets.
func TestNewFromEndpoints_ServesResponses(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	cc, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: s.URL, APIKey: "k", Model: "gpt-5", Alias: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", resp.Model)
	}
}

// Hand-built entries with no explicit alias keep working: the alias derives
// from the model name, and duplicates fall back to a positional identity.
func TestManualEntries_DeriveStableAliases(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	client := newClientForServer(t, s)

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "gpt-4o", Client: client},
		{Name: "gpt-4o", Client: client}, // same model: the canary case
		{Name: "", Client: client},
	})
	if err != nil {
		t.Fatal(err)
	}

	stats := cc.Stats()

	want := []string{"gpt-4o", "entry-1", "entry-2"}
	for i, alias := range want {
		if stats[i].Alias != alias {
			t.Fatalf("alias %d = %q, want %q", i, stats[i].Alias, alias)
		}
	}
}

func TestNewComposeClient_DuplicateExplicitAlias(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	_, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "x", Client: newClientForServer(t, s)},
		{Name: "m1", Alias: "x", Client: newClientForServer(t, s)},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("expected a duplicate-alias error, got %v", err)
	}
}

// The client must own its entries: deriving aliases may not write back into the
// caller's slice, and later mutations by the caller must not reach routing.
func TestNewComposeClient_DoesNotMutateCallerEntries(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	entries := []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s)},
		{Name: "m1", Client: newClientForServer(t, s)},
	}

	cc, err := newRoutingClient(t, router.StrategyFailover, entries)
	if err != nil {
		t.Fatal(err)
	}

	if entries[0].Alias != "" || entries[1].Alias != "" {
		t.Fatalf("the caller's entries were written back: %q, %q", entries[0].Alias, entries[1].Alias)
	}

	entries[0].Name = "mutated"

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m0" {
		t.Fatalf("the routing table followed the caller's mutation: model = %q", resp.Model)
	}
}

// Cost routing reads the entry's static pricing scaled by the request's own
// output cap, so the cap of the call that selects the pool's active endpoint
// decides which one it is. Two pools are needed, not two calls: selection
// happens once per pool, which is exactly what the active model changed.
func TestCostStrategy_OutputCapDecidesTheSelection(t *testing.T) {
	sInputHeavy, sOutputHeavy := newTestServer(t), newTestServer(t)
	defer sInputHeavy.Close()
	defer sOutputHeavy.Close()

	entries := []ModelEntry{
		{
			Name: "input-heavy", Alias: "input-heavy", Client: newClientForServer(t, sInputHeavy),
			Cost: &router.EndpointCost{InputPrice: 100, OutputPrice: 0},
		},
		{
			Name: "output-heavy", Alias: "output-heavy", Client: newClientForServer(t, sOutputHeavy),
			Cost: &router.EndpointCost{InputPrice: 0, OutputPrice: 1},
		},
	}

	// One output unit: the output-heavy endpoint is cheapest.
	small, err := newRoutingClient(t, router.StrategyCost, entries)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := small.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "output-heavy" {
		t.Fatalf("model = %q, want output-heavy", resp.Model)
	}

	// A large output cap makes the input-heavy endpoint cheapest instead.
	large, err := newRoutingClient(t, router.StrategyCost, entries)
	if err != nil {
		t.Fatal(err)
	}

	big := testRequest()
	cap1000 := 1000
	big.MaxCompletionTokens = &cap1000

	resp, err = large.ChatCompletions(context.Background(), big)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "input-heavy" {
		t.Fatalf("model with a large output cap = %q, want input-heavy", resp.Model)
	}

	// The pool that selected under a large cap keeps that endpoint even for a
	// call whose cap would have chosen the other one.
	resp, err = large.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "input-heavy" {
		t.Fatalf("model = %q, want the incumbent input-heavy", resp.Model)
	}
}

// A compose client nests as a Responses backend too.
func TestNestedComposeClient_ServesBothForms(t *testing.T) {
	s := newResponsesServer(t, nil)
	defer s.Close()

	inner, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "inner", Alias: "inner", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	outer, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "", Alias: "pool", Client: inner},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := outer.Responses(context.Background(), responsesRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "inner" {
		t.Fatalf("model = %q, want inner", resp.Model)
	}

	if errors.Is(err, router.ErrCapabilityNotSatisfied) {
		t.Fatal("a nested pool must take part in Responses routing")
	}
}
