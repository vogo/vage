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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/router"
)

// newTestServer echoes the requested model back in a valid Messages response
// and records whether the request carried tools.
func newTestServer(t *testing.T, gotTools *atomic.Bool) *httptest.Server {
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
			"id": "msg_1", "type": "message", "role": "assistant",
			"model":   request["model"],
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 2},
		})
	}))
}

// newStatusServer returns a server that always replies with the given HTTP
// status and an Anthropic-shaped error body, plus a hit counter.
func newStatusServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": "boom"},
		})
	}))

	return s, &hits
}

func newFailServer(t *testing.T) *httptest.Server {
	t.Helper()

	s, _ := newStatusServer(t, http.StatusInternalServerError)

	return s
}

// newStreamServer emits a minimal but valid Messages SSE stream.
func newStreamServer(t *testing.T) *httptest.Server {
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

		start, _ := json.Marshal(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant",
				"model": request["model"], "content": []any{},
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 0},
			},
		})

		_, _ = fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", start)
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
}

func newClientForServer(t *testing.T, server *httptest.Server) *anthropic.Client {
	t.Helper()

	return anthropic.NewClient("test-key",
		anthropic.WithBaseURL(server.URL), anthropic.WithHTTPClient(server.Client()))
}

func testRequest() *anthropic.MessagesRequest {
	return &anthropic.MessagesRequest{
		Model:     "placeholder",
		MaxTokens: 64,
		Messages: []anthropic.MessagesMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
}

// blocksMessage builds a message whose content is the block-array form.
func blocksMessage(t *testing.T, blocks ...anthropic.ContentBlock) anthropic.MessagesMessage {
	t.Helper()

	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}

	return anthropic.MessagesMessage{Role: "user", Content: raw}
}

// newRoutingClient builds a ComposeClient whose retry policy keeps these tests
// about routing rather than about waiting: the core's default policy retries a
// failing endpoint three times over 3.5s, which would say nothing extra here. A
// test that is about retrying or recovering passes its own policy, which wins
// because caller options are applied last.
func newRoutingClient(
	t *testing.T, strategy router.Strategy, entries []ModelEntry, opts ...router.Option,
) (*ComposeClient, error) {
	t.Helper()

	defaults := []router.Option{
		router.WithRetryPolicy(time.Nanosecond, 0),
		router.WithRecoverTime(time.Minute),
	}

	return NewComposeClient(strategy, entries, append(defaults, opts...)...)
}

// recordingObserver collects attempt results safely.
type recordingObserver struct {
	mu      sync.Mutex
	results []router.AttemptResult
}

func (r *recordingObserver) fn(res router.AttemptResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.results = append(r.results, res)
}

func (r *recordingObserver) seen() []router.AttemptResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]router.AttemptResult(nil), r.results...)
}

func TestNewComposeClient_Validation(t *testing.T) {
	if _, err := newRoutingClient(t, router.StrategyFailover, nil); err == nil {
		t.Fatal("expected an error for empty entries")
	}

	if _, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{{Name: "m0"}}); err == nil {
		t.Fatal("expected an error for a nil client")
	}
}

func TestMessages_RoutesAndOverridesTheModel(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "claude-opus-5", Alias: "primary", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := testRequest()

	resp, err := cc.Messages(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "claude-opus-5" {
		t.Fatalf("model = %q, want the endpoint's own claude-opus-5", resp.Model)
	}

	if request.Model != "placeholder" {
		t.Fatalf("the caller's request was mutated to %q", request.Model)
	}
}

// An entry without a name leaves the request's own model in place.
func TestMessages_EmptyEntryNameKeepsTheRequestModel(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Alias: "a", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "placeholder" {
		t.Fatalf("model = %q, want the request's own model", resp.Model)
	}
}

func TestMessages_FailoverAndAttribution(t *testing.T) {
	sFail, sOK := newFailServer(t), newTestServer(t, nil)
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

	resp, err := cc.Messages(context.Background(), testRequest())
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

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.ErrorCount != 1 || s.Active {
		t.Fatalf("stats after 5xx = %+v, want dead/1 and not active", s)
	}
}

func TestMessages_AllFailAggregatesByAlias(t *testing.T) {
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

	_, err = cc.Messages(context.Background(), testRequest())

	var multi *router.MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("expected *router.MultiError, got %T: %v", err, err)
	}

	if len(multi.Errors) != 2 || multi.Errors[0].Alias != "a" || multi.Errors[1].Alias != "b" {
		t.Fatalf("aliases = %+v, want a then b", multi.Errors)
	}

	// errors.As reaches the provider's own error through the aggregate, without
	// this package or the core naming that type.
	var apiErr *anthropic.HTTPError
	if !errors.As(err, &apiErr) {
		t.Fatal("expected errors.As to find *anthropic.HTTPError in the chain")
	}
}

func TestMessagesStream_FailsOverOnEstablishment(t *testing.T) {
	sFail, sStream := newFailServer(t), newStreamServer(t)
	defer sFail.Close()
	defer sStream.Close()

	obs := &recordingObserver{}

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "broken", Client: newClientForServer(t, sFail)},
		{Name: "m1", Alias: "streamer", Client: newClientForServer(t, sStream)},
	}, router.WithAttemptObserver(obs.fn))
	if err != nil {
		t.Fatal(err)
	}

	stream, err := cc.MessagesStream(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stream.Close() }()

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}

	if event.MessageStart == nil || event.MessageStart.Message.Model != "m1" {
		t.Fatalf("first event = %+v, want a message_start from m1", event)
	}

	results := obs.seen()
	if len(results) != 2 || !results[0].Stream || !results[1].Stream || !results[1].Success {
		t.Fatalf("observations = %+v, want two stream attempts ending in success", results)
	}
}

// Recovery end to end: a dead endpoint rejoins the candidate pool once the
// recover time elapses, without taking the pool back from its replacement.
func TestRecovery_DeadEndpointRejoinsWithoutDisplacingTheActive(t *testing.T) {
	// Short enough to elapse between calls, so the wrapper is exercised end to
	// end without injecting a clock.
	const recoverWindow = 20 * time.Millisecond

	var hits atomic.Int64

	sFlaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"message": "boom"}})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m0",
			"content": []any{map[string]any{"type": "text", "text": "recovered"}},
		})
	}))
	defer sFlaky.Close()

	sOK := newTestServer(t, nil)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "flaky", Client: newClientForServer(t, sFlaky)},
		{Name: "m1", Alias: "steady", Client: newClientForServer(t, sOK)},
	}, router.WithRecoverTime(recoverWindow))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.Messages(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.Active {
		t.Fatalf("stats after 5xx = %+v, want dead and not active", s)
	}

	time.Sleep(2 * recoverWindow)

	if s := cc.Stats()[0]; s.Status != router.StatusProbation || s.Active {
		t.Fatalf("stats after the recover window = %+v, want probation and not active", s)
	}

	// The incumbent keeps serving: recovery restores candidacy, not the crown.
	resp, err := cc.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model after recovery = %q, want the incumbent m1", resp.Model)
	}

	if s := cc.Stats()[1]; !s.Active {
		t.Fatalf("steady should still be the active endpoint, got %+v", s)
	}
}

// A 429 has no cooling window of its own any more: it walks the same retry path
// as every other retryable failure and then takes the endpoint out.
func TestRateLimited_RetriesThenDies(t *testing.T) {
	s429, hits429 := newStatusServer(t, http.StatusTooManyRequests)
	defer s429.Close()

	sOK := newTestServer(t, nil)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "limited", Client: newClientForServer(t, s429)},
		{Name: "m1", Alias: "ok", Client: newClientForServer(t, sOK)},
	}, router.WithRetryPolicy(time.Millisecond, 2), router.WithRecoverTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.Messages(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	// Three attempts: the first plus two retries.
	if hits429.Load() != 3 {
		t.Fatalf("the rate-limited endpoint was hit %d times, want 3", hits429.Load())
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.ErrorCount != 1 {
		t.Fatalf("stats after 429 = %+v, want dead/1", s)
	}

	if _, err := cc.Messages(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if hits429.Load() != 3 {
		t.Fatalf("the dead endpoint was contacted again (%d hits)", hits429.Load())
	}
}

// A 400 is retryable like every non-credential 4xx: it exhausts the retry
// policy, then marks the endpoint dead.
func TestRequestFailure_4xxDiesAfterRetries(t *testing.T) {
	s400, hits400 := newStatusServer(t, http.StatusBadRequest)
	defer s400.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "badreq", Client: newClientForServer(t, s400)},
	}, router.WithRetryPolicy(time.Millisecond, 1), router.WithRecoverTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.Messages(context.Background(), testRequest())

	var endpointErr *router.EndpointError
	if !errors.As(err, &endpointErr) || endpointErr.Alias != "badreq" {
		t.Fatalf("expected an attributed EndpointError for badreq, got %v", err)
	}

	if hits400.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (the first plus one retry)", hits400.Load())
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.ErrorCount != 1 || s.LastError == nil {
		t.Fatalf("stats after 4xx = %+v, want dead/1 with the recorded failure", s)
	}
}

// Cost routing reads the entry's static pricing scaled by the request's own
// max_tokens, so the cap of the call that selects the pool's active endpoint
// decides which one it is.
func TestCostStrategy_OutputCapDecidesTheSelection(t *testing.T) {
	sInputHeavy, sOutputHeavy := newTestServer(t, nil), newTestServer(t, nil)
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

	// MaxTokens=64: output-heavy cost is 64, input-heavy is 100 → output-heavy.
	small, err := newRoutingClient(t, router.StrategyCost, entries)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := small.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "output-heavy" {
		t.Fatalf("model = %q, want output-heavy", resp.Model)
	}

	// A large max_tokens makes the input-heavy endpoint cheapest instead.
	large, err := newRoutingClient(t, router.StrategyCost, entries)
	if err != nil {
		t.Fatal(err)
	}

	big := testRequest()
	big.MaxTokens = 1000

	resp, err = large.Messages(context.Background(), big)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "input-heavy" {
		t.Fatalf("model with a large output cap = %q, want input-heavy", resp.Model)
	}

	// The pool that selected under a large cap keeps that endpoint even for a
	// call whose cap would have chosen the other one.
	resp, err = large.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "input-heavy" {
		t.Fatalf("model = %q, want the incumbent input-heavy", resp.Model)
	}
}

// 401 is the one failure that skips the retries entirely.
func TestCredentialFailure_401DiesWithoutRetrying(t *testing.T) {
	s401, hits401 := newStatusServer(t, http.StatusUnauthorized)
	defer s401.Close()

	sOK := newTestServer(t, nil)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "expired", Client: newClientForServer(t, s401)},
		{Name: "m1", Alias: "ok", Client: newClientForServer(t, sOK)},
	}, router.WithRetryPolicy(time.Minute, 5), router.WithRecoverTime(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want m1", resp.Model)
	}

	// One attempt only — the minute-long retry policy is configured precisely so
	// a single retry would hang this test rather than pass slowly.
	if hits401.Load() != 1 {
		t.Fatalf("attempts on the expired endpoint = %d, want 1", hits401.Load())
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead {
		t.Fatalf("stats after 401 = %+v, want dead", s)
	}
}

func TestNoActiveModels_Error(t *testing.T) {
	s := newFailServer(t)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = cc.Messages(context.Background(), testRequest())

	if _, err := cc.Messages(context.Background(), testRequest()); !errors.Is(err, router.ErrNoActiveModels) {
		t.Fatalf("expected router.ErrNoActiveModels, got %v", err)
	}
}

// Every strategy routes through the same core loop, so each one fails over.
func TestEveryStrategy_FailsOverToTheHealthyEndpoint(t *testing.T) {
	strategies := []router.Strategy{
		router.StrategyFailover,
		router.StrategyRandom,
		router.StrategyWeight,
		router.StrategyCost,
		router.StrategyLatency,
	}

	for _, strategy := range strategies {
		t.Run(string(strategy), func(t *testing.T) {
			sFail, sOK := newFailServer(t), newTestServer(t, nil)
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

			resp, err := cc.Messages(context.Background(), testRequest())
			if err != nil {
				t.Fatalf("%s: expected failover to succeed, got %v", strategy, err)
			}

			if resp.Model != "m1" {
				t.Fatalf("%s: model = %q, want m1", strategy, resp.Model)
			}
		})
	}
}

// The pool serves from one active endpoint: successive calls stay on it without
// the caller carrying any affinity key.
func TestActiveEndpoint_SuccessiveCallsStayOnOneBackend(t *testing.T) {
	entries := make([]ModelEntry, 3)

	for i, alias := range []string{"a", "b", "c"} {
		s := newTestServer(t, nil)
		t.Cleanup(s.Close)

		entries[i] = ModelEntry{Name: "m" + alias, Alias: alias, Client: newClientForServer(t, s)}
	}

	// Random is the interesting case: with the active model even a randomising
	// strategy chooses once, when the pool needs an endpoint, not per call.
	cc, err := newRoutingClient(t, router.StrategyRandom, entries)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	first, err := cc.Messages(ctx, testRequest())
	if err != nil {
		t.Fatal(err)
	}

	for range 10 {
		resp, err := cc.Messages(ctx, testRequest())
		if err != nil {
			t.Fatal(err)
		}

		if resp.Model != first.Model {
			t.Fatalf("the active endpoint drifted: %q then %q", first.Model, resp.Model)
		}
	}

	// The entries are named "m<alias>", so the alias is the model name's tail.
	wantAlias := first.Model[1:]

	for _, stat := range cc.Stats() {
		if (stat.Alias == wantAlias) != stat.Active {
			t.Fatalf("Stats disagrees about the active endpoint: %+v (serving %q)", stat, wantAlias)
		}
	}
}

func TestNestedComposeClients(t *testing.T) {
	sFail, sOK := newFailServer(t), newTestServer(t, nil)
	defer sFail.Close()
	defer sOK.Close()

	inner, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "inner", Client: newClientForServer(t, sFail)},
	})
	if err != nil {
		t.Fatal(err)
	}

	outer, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "pool", Client: inner},
		{Name: "m1", Client: newClientForServer(t, sOK)},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := outer.Messages(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want the outer fallback m1", resp.Model)
	}
}

// A pool serves one conversation: concurrent callers do not queue, they are
// rejected with router.ErrCallInProgress and the pool stays usable.
func TestConcurrentRequests_AreRejectedNotQueued(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg       sync.WaitGroup
		served   atomic.Int64
		rejected atomic.Int64
	)

	errCh := make(chan error, 50)

	for range 50 {
		wg.Go(func() {
			switch _, err := cc.Messages(context.Background(), testRequest()); {
			case err == nil:
				served.Add(1)
			case errors.Is(err, router.ErrCallInProgress):
				rejected.Add(1)
			default:
				errCh <- err
			}

			// Stats never queues behind a call, so it answers throughout.
			_ = cc.Stats()
		})
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("unexpected concurrent request error: %v", err)
	}

	if served.Load()+rejected.Load() != 50 {
		t.Fatalf("served %d + rejected %d, want 50 accounted for", served.Load(), rejected.Load())
	}

	if served.Load() == 0 {
		t.Fatal("no request was served at all")
	}

	// The pool is usable again once the storm passes.
	if _, err := cc.Messages(context.Background(), testRequest()); err != nil {
		t.Fatalf("pool unusable after concurrent rejection: %v", err)
	}
}

func TestContextCancellation_DoesNotPoisonHealth(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cc.Messages(ctx, testRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if s := cc.Stats()[0]; s.Status != router.StatusAvailable {
		t.Fatalf("status after cancellation = %q, want available", s.Status)
	}
}

func TestNewFromEndpoints_ConstructionAndCredentials(t *testing.T) {
	var gotAuth, gotBeta string

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-api-key")
		gotBeta = r.Header.Get("anthropic-beta")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []any{map[string]any{"type": "text", "text": "ok"}},
		})
	}))
	defer s.Close()

	cc, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: s.URL, APIKey: "secret-key", Model: "m", Alias: "a", Beta: []string{"my-beta"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.Messages(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotAuth, "secret-key") {
		t.Fatalf("the endpoint did not receive its explicit key, x-api-key = %q", gotAuth)
	}

	if !strings.Contains(gotBeta, "my-beta") {
		t.Fatalf("per-endpoint beta not applied, anthropic-beta = %q", gotBeta)
	}
}

func TestNewFromEndpoints_AliasValidation(t *testing.T) {
	if _, err := NewFromEndpoints(router.StrategyFailover, nil); err == nil {
		t.Fatal("expected an error for empty specs")
	}

	_, err := NewFromEndpoints(router.StrategyFailover, []EndpointSpec{{BaseURL: "http://x", Model: "m"}})
	if err == nil || !strings.Contains(err.Error(), "alias is required") {
		t.Fatalf("expected a missing-alias error, got %v", err)
	}

	_, err = NewFromEndpoints(router.StrategyFailover, []EndpointSpec{
		{BaseURL: "http://x", Model: "m", Alias: "dup"},
		{BaseURL: "http://y", Model: "m", Alias: "dup"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("expected a duplicate-alias error, got %v", err)
	}
}

// Hand-built entries with no explicit alias keep working: the alias derives
// from the model name, and duplicates fall back to a positional identity.
func TestManualEntries_DeriveStableAliases(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	client := newClientForServer(t, s)

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "claude-opus-5", Client: client},
		{Name: "claude-opus-5", Client: client},
		{Name: "", Client: client},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"claude-opus-5", "entry-1", "entry-2"}
	for i, alias := range want {
		if got := cc.Stats()[i].Alias; got != alias {
			t.Fatalf("alias %d = %q, want %q", i, got, alias)
		}
	}
}

func TestNewComposeClient_DoesNotMutateCallerEntries(t *testing.T) {
	s := newTestServer(t, nil)
	defer s.Close()

	entries := []ModelEntry{{Name: "m0", Client: newClientForServer(t, s)}}

	if _, err := newRoutingClient(t, router.StrategyFailover, entries); err != nil {
		t.Fatal(err)
	}

	if entries[0].Alias != "" {
		t.Fatalf("the caller's entry was written back: %q", entries[0].Alias)
	}
}
