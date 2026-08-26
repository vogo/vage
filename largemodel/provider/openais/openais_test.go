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
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/router"
)

// newTestServer creates an httptest server that echoes the requested model back
// in a valid Chat Completions response.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		model := fmt.Sprintf("%v", request["model"])

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID:    "test-id",
			Model: model,
			Choices: []openai.ChatCompletionChoice{{
				Index:   0,
				Message: openai.ChatCompletionMessage{Role: "assistant", Content: openai.NewTextContent("hello from " + model)},
			}},
		})
	}))
}

// newStatusServer returns a server that always replies with the given HTTP
// status and an OpenAI-shaped error body, plus a hit counter.
func newStatusServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "boom", "type": "error"},
		})
	}))

	return s, &hits
}

// newFailServer creates a server that always returns a 500 error.
func newFailServer(t *testing.T) *httptest.Server {
	t.Helper()

	s, _ := newStatusServer(t, http.StatusInternalServerError)

	return s
}

// newStreamServer creates a server that returns a valid chat SSE stream.
func newStreamServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)

			return
		}

		chunk := openai.ChatCompletionChunk{
			ID:    "chunk-1",
			Model: fmt.Sprintf("%v", request["model"]),
			Choices: []openai.ChatCompletionChunkChoice{{
				Index: 0,
				Delta: openai.ChatCompletionMessage{Role: "assistant", Content: openai.NewTextContent("hello stream")},
			}},
		}

		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func newClientForServer(t *testing.T, server *httptest.Server) *openai.Client {
	t.Helper()

	return openai.NewClient("test-key", openai.WithBaseURL(server.URL), openai.WithHTTPClient(server.Client()))
}

func testRequest() *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model: "placeholder",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: openai.NewTextContent("hi")},
		},
	}
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

func TestNewComposeClient_EmptyEntries(t *testing.T) {
	if _, err := newRoutingClient(t, router.StrategyFailover, nil); err == nil {
		t.Fatal("expected an error for empty entries")
	}
}

func TestNewComposeClient_NilClient(t *testing.T) {
	_, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{{Name: "m0", Client: nil}})
	if err == nil {
		t.Fatal("expected an error for a nil client")
	}
}

// An entry without a name leaves the request's own model alone. There is no
// client-level default model to fall back to.
func TestEmptyEntryName_KeepsTheRequestModel(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if response.Model != "placeholder" {
		t.Fatalf("model = %s, want the request's own model", response.Model)
	}
}

func TestFailover_FirstEndpointSucceeds(t *testing.T) {
	s0, s1 := newTestServer(t), newTestServer(t)
	defer s0.Close()
	defer s1.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s0)},
		{Name: "m1", Client: newClientForServer(t, s1)},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Model != "m0" {
		t.Fatalf("model = %s, want m0", resp.Model)
	}
}

func TestFailover_Fallback(t *testing.T) {
	sFail, sOK := newFailServer(t), newTestServer(t)
	defer sFail.Close()
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, sFail)},
		{Name: "m1", Client: newClientForServer(t, sOK)},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %s, want m1", resp.Model)
	}
}

func TestFailover_AllFailAttributesEveryAlias(t *testing.T) {
	s0, s1 := newFailServer(t), newFailServer(t)
	defer s0.Close()
	defer s1.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, s0)},
		{Name: "m1", Client: newClientForServer(t, s1)},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.ChatCompletions(context.Background(), testRequest())

	var multi *router.MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("expected *router.MultiError, got %T: %v", err, err)
	}

	if len(multi.Errors) != 2 {
		t.Fatalf("endpoint errors = %d, want 2", len(multi.Errors))
	}

	if multi.Errors[0].Alias != "m0" || multi.Errors[1].Alias != "m1" {
		t.Fatalf("aliases = %q, %q; want m0, m1", multi.Errors[0].Alias, multi.Errors[1].Alias)
	}

	// errors.As reaches the endpoint attribution and the provider's own error.
	var endpointErr *router.EndpointError
	if !errors.As(err, &endpointErr) {
		t.Fatal("expected errors.As to find a router.EndpointError in the chain")
	}

	var apiErr *openai.HTTPError
	if !errors.As(err, &apiErr) {
		t.Fatal("expected errors.As to find *openai.HTTPError in the chain")
	}
}

func TestFailover_StreamFallback(t *testing.T) {
	sFail, sStream := newFailServer(t), newStreamServer(t)
	defer sFail.Close()
	defer sStream.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Client: newClientForServer(t, sFail)},
		{Name: "m1", Client: newClientForServer(t, sStream)},
	})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := cc.ChatCompletionsStream(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	defer func() { _ = stream.Close() }()

	chunk, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}

	if chunk.Model != "m1" {
		t.Fatalf("stream model = %s, want m1", chunk.Model)
	}

	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
	}
}

// Recovery end to end: a dead endpoint returns to the candidate pool once the
// recover time elapses, and the endpoint that replaced it keeps serving.
func TestRecovery_DeadEndpointRejoinsWithoutDisplacingTheActive(t *testing.T) {
	var hits atomic.Int64

	sFlaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "boom"}})

			return
		}

		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "id", Model: "m0",
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{Role: "assistant", Content: openai.NewTextContent("recovered")},
			}},
		})
	}))
	defer sFlaky.Close()

	sOK := newTestServer(t)
	defer sOK.Close()

	// A recover time of a few milliseconds elapses on its own between calls, so
	// the wrapper is exercised end to end without injecting a clock.
	const recover = 20 * time.Millisecond

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "flaky", Client: newClientForServer(t, sFlaky)},
		{Name: "m1", Alias: "steady", Client: newClientForServer(t, sOK)},
	}, router.WithRecoverTime(recover))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %s, want the failover m1", resp.Model)
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.ErrorCount != 1 || s.Active {
		t.Fatalf("stats after 5xx = %+v, want dead/1/not-active", s)
	}

	time.Sleep(2 * recover)

	// The recovered endpoint is selectable again, on probation until a call
	// confirms it...
	if s := cc.Stats()[0]; s.Status != router.StatusProbation || s.Active {
		t.Fatalf("stats after the recover window = %+v, want probation and not active", s)
	}

	// ...but the healthy incumbent keeps serving: recovery is not a takeover.
	resp, err = cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model after recovery = %s, want the incumbent m1", resp.Model)
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

	sOK := newTestServer(t)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "limited", Client: newClientForServer(t, s429)},
		{Name: "m1", Alias: "ok", Client: newClientForServer(t, sOK)},
	}, router.WithRetryPolicy(time.Millisecond, 2), router.WithRecoverTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want m1 (failover from the dead endpoint)", resp.Model)
	}

	// Three attempts: the first plus two retries.
	if hits429.Load() != 3 {
		t.Fatalf("the rate-limited endpoint was hit %d times, want 3", hits429.Load())
	}

	if s := cc.Stats()[0]; s.Status != router.StatusDead || s.ErrorCount != 1 {
		t.Fatalf("stats after 429 = %+v, want dead/1", s)
	}

	// It stays out of rotation for the whole recover window.
	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	if hits429.Load() != 3 {
		t.Fatalf("the dead endpoint was contacted again (%d hits)", hits429.Load())
	}
}

// A 400 no longer leaves the endpoint healthy: every non-credential failure is
// retried and then judged.
func TestRequestFailure_4xxDiesAfterRetries(t *testing.T) {
	s400, hits400 := newStatusServer(t, http.StatusBadRequest)
	defer s400.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "badreq", Client: newClientForServer(t, s400)},
	}, router.WithRetryPolicy(time.Millisecond, 1), router.WithRecoverTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	_, err = cc.ChatCompletions(context.Background(), testRequest())

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

// 401 is the one failure that skips the retries entirely.
func TestCredentialFailure_401DiesWithoutRetrying(t *testing.T) {
	s401, hits401 := newStatusServer(t, http.StatusUnauthorized)
	defer s401.Close()

	sOK := newTestServer(t)
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "expired", Client: newClientForServer(t, s401)},
		{Name: "m1", Alias: "ok", Client: newClientForServer(t, sOK)},
	}, router.WithRetryPolicy(time.Minute, 5), router.WithRecoverTime(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Model != "m1" {
		t.Fatalf("model = %q, want m1", resp.Model)
	}

	// One attempt only — a minute-long retry policy was configured precisely so
	// that a single retry would make this test hang rather than pass slowly.
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

	// The first call fails and errors the only endpoint.
	_, _ = cc.ChatCompletions(context.Background(), testRequest())

	// The second finds no candidate at all.
	if _, err = cc.ChatCompletions(context.Background(), testRequest()); !errors.Is(err, router.ErrNoActiveModels) {
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
			sFail, sOK := newFailServer(t), newTestServer(t)
			defer sFail.Close()
			defer sOK.Close()

			cheap, slow := 10*time.Millisecond, 50*time.Millisecond

			cc, err := newRoutingClient(t, strategy, []ModelEntry{
				{
					Name: "m0", Alias: "broken", Weight: 9, Client: newClientForServer(t, sFail),
					Cost: &router.EndpointCost{InputPrice: 1, OutputPrice: 1}, Latency: &cheap,
				},
				{
					Name: "m1", Alias: "healthy", Weight: 1, Client: newClientForServer(t, sOK),
					Cost: &router.EndpointCost{InputPrice: 5, OutputPrice: 5}, Latency: &slow,
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			resp, err := cc.ChatCompletions(context.Background(), testRequest())
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
	obs := &recordingObserver{}

	entries := make([]ModelEntry, 3)

	for i, alias := range []string{"a", "b", "c"} {
		s := newTestServer(t)
		t.Cleanup(s.Close)

		entries[i] = ModelEntry{Name: "m" + alias, Alias: alias, Client: newClientForServer(t, s)}
	}

	// Random is the interesting case: with the active model even a randomising
	// strategy chooses once, when the pool needs an endpoint, not per call.
	cc, err := newRoutingClient(t, router.StrategyRandom, entries, router.WithAttemptObserver(obs.fn))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	first, err := cc.ChatCompletions(ctx, testRequest())
	if err != nil {
		t.Fatal(err)
	}

	for range 10 {
		resp, err := cc.ChatCompletions(ctx, testRequest())
		if err != nil {
			t.Fatal(err)
		}

		if resp.Model != first.Model {
			t.Fatalf("the active endpoint drifted: %q then %q", first.Model, resp.Model)
		}
	}

	// Every attempt was attributed to the same alias, and all succeeded. The
	// entries are named "m<alias>", so the alias is the model name's tail.
	wantAlias := first.Model[1:]

	for _, res := range obs.seen() {
		if !res.Success || res.Alias != wantAlias {
			t.Fatalf("unexpected attempt %+v, want a success on alias %q", res, wantAlias)
		}
	}

	for _, stat := range cc.Stats() {
		if (stat.Alias == wantAlias) != stat.Active {
			t.Fatalf("Stats disagrees about the active endpoint: %+v (serving %q)", stat, wantAlias)
		}
	}
}

func TestModelOverride_LeavesTheCallerRequestUntouched(t *testing.T) {
	var received atomic.Value

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)

		model := fmt.Sprintf("%v", request["model"])
		received.Store(model)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "test", Model: model})
	}))
	defer s.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "custom-model-v2", Client: newClientForServer(t, s)},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := testRequest()
	request.Model = "original-model"

	if _, err = cc.ChatCompletions(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	if got := received.Load(); got != "custom-model-v2" {
		t.Fatalf("sent model = %v, want custom-model-v2", got)
	}

	if request.Model != "original-model" {
		t.Fatalf("the caller's request was mutated to %s", request.Model)
	}
}

// A ComposeClient is itself a backend, so pools nest (for example a fast pool
// that falls back to a slower one).
func TestNestedComposeClients(t *testing.T) {
	sFail, sOK := newFailServer(t), newTestServer(t)
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

	response, err := outer.ChatCompletions(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	if response.Model != "m1" {
		t.Fatalf("model = %s, want the outer fallback m1", response.Model)
	}
}

// A pool serves one conversation: concurrent callers do not queue, they are
// rejected with router.ErrCallInProgress and the pool stays usable.
func TestConcurrentRequests_AreRejectedNotQueued(t *testing.T) {
	s := newTestServer(t)
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
			switch _, err := cc.ChatCompletions(context.Background(), testRequest()); {
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
	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatalf("pool unusable after concurrent rejection: %v", err)
	}
}

func TestContextCancellation_DoesNotPoisonHealth(t *testing.T) {
	s := newTestServer(t)
	defer s.Close()

	obs := &recordingObserver{}

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "m0", Client: newClientForServer(t, s)},
	}, router.WithAttemptObserver(obs.fn))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err = cc.ChatCompletions(ctx, testRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if s := cc.Stats()[0]; s.Status != router.StatusAvailable {
		t.Fatalf("status after cancellation = %q, want available", s.Status)
	}
}

func TestAttemptObserver_AttributesFailoverAndStreams(t *testing.T) {
	sFail, sStream := newFailServer(t), newStreamServer(t)
	defer sFail.Close()
	defer sStream.Close()

	obs := &recordingObserver{}

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "bad", Client: newClientForServer(t, sFail)},
		{Name: "m1", Alias: "good", Client: newClientForServer(t, sStream)},
	}, router.WithAttemptObserver(obs.fn))
	if err != nil {
		t.Fatal(err)
	}

	stream, err := cc.ChatCompletionsStream(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}

	_ = stream.Close()

	results := obs.seen()
	if len(results) != 2 {
		t.Fatalf("observations = %d, want 2", len(results))
	}

	if results[0].Alias != "bad" || results[0].Success || !results[0].Stream {
		t.Fatalf("first observation = %+v, want bad/failure/stream", results[0])
	}

	if results[1].Alias != "good" || !results[1].Success || !results[1].Stream {
		t.Fatalf("second observation = %+v, want good/success/stream", results[1])
	}
}

// Both interaction forms share one pool, so the health a chat failure records
// is the health Responses routing sees.
func TestStats_SharedAcrossInteractionForms(t *testing.T) {
	sFail, sOK := newFailServer(t), newTestServer(t)
	defer sFail.Close()
	defer sOK.Close()

	cc, err := newRoutingClient(t, router.StrategyFailover, []ModelEntry{
		{Name: "m0", Alias: "broken", Client: newClientForServer(t, sFail)},
		{Name: "m1", Alias: "healthy", Client: newClientForServer(t, sOK)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cc.ChatCompletions(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	stats := cc.Stats()
	if len(stats) != 2 {
		t.Fatalf("stats len = %d, want 2", len(stats))
	}

	byAlias := map[string]router.EndpointStat{}
	for _, s := range stats {
		byAlias[s.Alias] = s
	}

	if byAlias["broken"].Status != router.StatusDead || byAlias["broken"].Active {
		t.Fatalf("broken stats = %+v, want dead and not active", byAlias["broken"])
	}

	if byAlias["healthy"].Status != router.StatusAvailable || !byAlias["healthy"].Active ||
		byAlias["healthy"].LastError != nil {
		t.Fatalf("healthy stats = %+v, want available, active, with zeroed error fields", byAlias["healthy"])
	}
}
