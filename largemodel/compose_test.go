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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/provider/anthropics"
	"github.com/vogo/vage/largemodel/provider/openais"
	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/vage/schema"
)

// These tests cover the multi-endpoint callers, whose routing, retries and
// health tracking belong to largemodel/router rather than to vage middlewares.
// What vage owns on this path is the pool set that makes a router pool — which
// serves one call at a time — safe behind a Caller several agents share.

// fastRouting keeps a test pool from sleeping through the default backoff.
func fastRouting() ComposeOption {
	return func(c *composeConfig) {
		c.routerOpts = append(c.routerOpts,
			router.WithRetryPolicy(time.Millisecond, 0),
			router.WithRecoverTime(time.Minute),
		)
	}
}

func openAIEndpointsFromSpecs(specs []openais.EndpointSpec) []OpenAIEndpoint {
	endpoints := make([]OpenAIEndpoint, len(specs))
	for i, s := range specs {
		endpoints[i] = OpenAIEndpoint{
			Alias: s.Alias, APIKey: s.APIKey, BaseURL: s.BaseURL, Model: s.Model,
			Weight: s.Weight, Tags: s.Tags, Cost: s.Cost, Latency: s.Latency,
		}
	}
	return endpoints
}

func mustOpenAIComposeCaller(t *testing.T, strategy Strategy, specs []openais.EndpointSpec, opts ...ComposeOption) *OpenAIChatComposeCaller {
	t.Helper()

	caller, err := NewOpenAIChatCallerFromConfig(OpenAIConfig{
		Strategy: strategy, Endpoints: openAIEndpointsFromSpecs(specs),
	}, opts...)
	if err != nil {
		t.Fatalf("NewOpenAIChatCallerFromConfig: %v", err)
	}

	return caller
}

func mustAnthropicComposeCaller(t *testing.T, strategy Strategy, specs []anthropics.EndpointSpec, opts ...ComposeOption) *AnthropicMessagesComposeCaller {
	t.Helper()

	endpoints := make([]AnthropicEndpoint, len(specs))
	for i, s := range specs {
		endpoints[i] = AnthropicEndpoint{
			Alias: s.Alias, APIKey: s.APIKey, BaseURL: s.BaseURL, Model: s.Model,
			Weight: s.Weight, Tags: s.Tags, Version: s.Version, Beta: s.Beta,
			Cost: s.Cost, Latency: s.Latency,
		}
	}

	caller, err := NewAnthropicMessagesCallerFromConfig(AnthropicConfig{
		Strategy: strategy, Endpoints: endpoints,
	}, opts...)
	if err != nil {
		t.Fatalf("NewAnthropicMessagesCallerFromConfig: %v", err)
	}

	return caller
}

// countingServer replies with status and body, counting the requests it saw
// and recording the model each one asked for.
type countingServer struct {
	*httptest.Server

	hits   atomic.Int64
	models chan string
}

func newCountingServer(t *testing.T, status int, body string, delay time.Duration) *countingServer {
	t.Helper()

	cs := &countingServer{models: make(chan string, 64)}

	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.hits.Add(1)

		var payload struct {
			Model string `json:"model"`
		}

		if raw, err := io.ReadAll(r.Body); err == nil {
			_ = json.Unmarshal(raw, &payload)
		}

		select {
		case cs.models <- payload.Model:
		default:
		}

		if delay > 0 {
			time.Sleep(delay)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(cs.Close)

	return cs
}

const openAITextReply = `{"id":"cmpl-1","object":"chat.completion","model":"gpt-4",
	"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

const anthropicTextReply = `{"id":"msg-1","type":"message","role":"assistant","model":"claude",
	"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
	"usage":{"input_tokens":10,"output_tokens":5}}`

// openAISpec is one endpoint pointed at a test server.
func openAISpec(alias, baseURL, model string) openais.EndpointSpec {
	return openais.EndpointSpec{
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   model,
		Alias:   alias,
	}
}

// TestComposeCaller_Failover checks that a failing endpoint is deserted for
// the next one and that the reply comes back through vage's envelopes intact.
func TestComposeCaller_Failover(t *testing.T) {
	bad := newCountingServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, 0)
	good := newCountingServer(t, http.StatusOK, openAITextReply, 0)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("primary", bad.URL, "model-a"),
		openAISpec("secondary", good.URL, "model-b"),
	}, fastRouting())

	resp, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if got := resp.Message.Text(); got != "hello" {
		t.Errorf("Text() = %q, want %q", got, "hello")
	}

	if bad.hits.Load() == 0 {
		t.Error("primary endpoint was never attempted")
	}

	if good.hits.Load() != 1 {
		t.Errorf("secondary hits = %d, want 1", good.hits.Load())
	}

	// The endpoint that failed is dead; the one that served the call is now
	// the pool's active endpoint.
	stats := caller.Stats()
	if len(stats) != 2 {
		t.Fatalf("len(Stats()) = %d, want 2", len(stats))
	}

	if stats[0].Alias != "primary" || stats[0].Status != router.StatusDead {
		t.Errorf("primary stat = %+v, want alias=primary status=dead", stats[0])
	}

	if stats[1].Alias != "secondary" || !stats[1].Active {
		t.Errorf("secondary stat = %+v, want alias=secondary active", stats[1])
	}
}

// TestComposeCaller_EndpointModelOverride pins that each endpoint sends the
// model it was configured with rather than the one the request named, which
// is what lets one logical model span backends that spell it differently.
func TestComposeCaller_EndpointModelOverride(t *testing.T) {
	srv := newCountingServer(t, http.StatusOK, openAITextReply, 0)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("only", srv.URL, "vendor-model-name"),
	}, fastRouting())

	if _, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat)); err != nil {
		t.Fatalf("Call: %v", err)
	}

	select {
	case got := <-srv.models:
		if got != "vendor-model-name" {
			t.Errorf("model sent = %q, want %q", got, "vendor-model-name")
		}
	default:
		t.Fatal("server recorded no request")
	}
}

// TestComposeCaller_AllEndpointsFail checks that a dispatch which tried every
// endpoint still reaches vage's middlewares as a judgeable APIError: aimodel
// aggregates the attempts, and errors.As has to see through the aggregate to
// the vendor status underneath.
func TestComposeCaller_AllEndpointsFail(t *testing.T) {
	first := newCountingServer(t, http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, 0)
	second := newCountingServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, 0)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("first", first.URL, "model-a"),
		openAISpec("second", second.URL, "model-b"),
	}, fastRouting())

	_, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
	if err == nil {
		t.Fatal("Call succeeded, want failure")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}

	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusTooManyRequests)
	}

	// Both endpoints were tried, and the aggregate is still reachable.
	var multi *router.MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("error %v does not carry a *router.MultiError", err)
	}

	if len(multi.Errors) != 2 {
		t.Errorf("len(MultiError.Errors) = %d, want 2", len(multi.Errors))
	}
}

// TestComposeCaller_Concurrent is the reason the pool set exists: an aimodel
// pool rejects a second concurrent call with ErrCallInProgress, and vage
// shares one Caller across agents that run in parallel.
func TestComposeCaller_Concurrent(t *testing.T) {
	srv := newCountingServer(t, http.StatusOK, openAITextReply, 20*time.Millisecond)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("only", srv.URL, "model-a"),
	}, fastRouting())

	const callers = 8

	var (
		wg   sync.WaitGroup
		errs = make(chan error, callers)
	)

	for range callers {
		wg.Go(func() {
			_, callErr := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
			if callErr != nil {
				errs <- callErr
			}
		})
	}

	wg.Wait()
	close(errs)

	for callErr := range errs {
		if errors.Is(callErr, router.ErrCallInProgress) {
			t.Fatalf("concurrent call was rejected as busy: %v", callErr)
		}

		t.Fatalf("Call: %v", callErr)
	}

	if got := srv.hits.Load(); got != callers {
		t.Errorf("server hits = %d, want %d", got, callers)
	}
}

// TestComposeCaller_ConcurrencyLimitWaits checks that exceeding the pool limit
// makes a call wait rather than fail: with one pool, two concurrent calls are
// served one after the other.
func TestComposeCaller_ConcurrencyLimitWaits(t *testing.T) {
	srv := newCountingServer(t, http.StatusOK, openAITextReply, 20*time.Millisecond)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("only", srv.URL, "model-a"),
	}, fastRouting(), WithComposeConcurrency(1))

	var (
		wg   sync.WaitGroup
		errs = make(chan error, 2)
	)

	for range 2 {
		wg.Go(func() {
			_, callErr := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
			if callErr != nil {
				errs <- callErr
			}
		})
	}

	wg.Wait()
	close(errs)

	for callErr := range errs {
		t.Fatalf("Call: %v", callErr)
	}

	if got := srv.hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
}

// TestComposeCaller_WaitRespectsContext checks that a call queued behind a
// busy pool gives up when its context does, rather than blocking forever.
func TestComposeCaller_WaitRespectsContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, openAITextReply); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))

	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("only", srv.URL, "model-a"),
	}, fastRouting(), WithComposeConcurrency(1))

	blocked := make(chan struct{})

	go func() {
		defer close(blocked)

		_, _ = caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
	}()

	// Wait for the only pool to be borrowed by the blocked call.
	waitFor(t, func() bool {
		select {
		case c := <-caller.pool.idle:
			caller.pool.idle <- c

			return false
		default:
			return true
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := caller.Call(ctx, simpleRequest(schema.ProtocolOpenAIChat)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error = %v, want context.DeadlineExceeded", err)
	}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("condition not met within timeout")
}

// TestComposeCaller_StreamReleasesPool checks that a pool borrowed to open a
// stream is handed back once the stream is established — router dispatch
// covers establishment only — so a second call is not blocked behind a stream
// the consumer has not finished reading.
func TestComposeCaller_StreamReleasesPool(t *testing.T) {
	streamBody := sse(
		"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`+"\n\n",
		"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n",
		"data: [DONE]\n\n",
	)

	// The server answers streaming and plain calls differently, because this
	// test makes both: the plain one is what proves the pool came back.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}

		if raw, err := io.ReadAll(r.Body); err == nil {
			_ = json.Unmarshal(raw, &payload)
		}

		body := openAITextReply
		contentType := "application/json"

		if payload.Stream {
			body = streamBody
			contentType = "text/event-stream"
		}

		w.Header().Set("Content-Type", contentType)

		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		openAISpec("only", srv.URL, "model-a"),
	}, fastRouting(), WithComposeConcurrency(1))

	stream, err := caller.CallStream(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	defer func() { _ = stream.Close() }()

	// The single pool is back in the idle set even though the stream is still
	// open, so a plain call goes through without waiting.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := caller.Call(ctx, simpleRequest(schema.ProtocolOpenAIChat)); err != nil {
		t.Fatalf("Call while streaming: %v", err)
	}

	var acc StreamAccumulator

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}

		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}

		acc.Add(chunk)
	}

	if got := acc.Text(); got != "hello" {
		t.Errorf("streamed text = %q, want %q", got, "hello")
	}
}

// TestAnthropicComposeCaller_Failover covers the Anthropic half: a separate
// pool over the same routing core, with its own health and no cross-protocol
// failover.
func TestAnthropicComposeCaller_Failover(t *testing.T) {
	bad := newCountingServer(t, http.StatusInternalServerError,
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`, 0)
	good := newCountingServer(t, http.StatusOK, anthropicTextReply, 0)

	caller := mustAnthropicComposeCaller(t, StrategyFailover, []anthropics.EndpointSpec{
		{BaseURL: bad.URL, APIKey: "test-key", Model: "claude-a", Alias: "primary"},
		{BaseURL: good.URL, APIKey: "test-key", Model: "claude-b", Alias: "secondary"},
	}, fastRouting())

	resp, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolAnthropicMessages))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if got := resp.Message.Text(); got != "hello" {
		t.Errorf("Text() = %q, want %q", got, "hello")
	}

	if caller.Protocol() != schema.ProtocolAnthropicMessages {
		t.Errorf("Protocol() = %q, want %q", caller.Protocol(), schema.ProtocolAnthropicMessages)
	}

	stats := caller.Stats()
	if len(stats) != 2 || stats[0].Status != router.StatusDead {
		t.Errorf("Stats() = %+v, want primary dead", stats)
	}
}

// TestSingleEndpointCaller_IsAPoolOfOne pins that the plain constructors route
// through the router too: one endpoint, retried in place and then judged dead,
// with no second candidate to fail over to.
func TestSingleEndpointCaller_IsAPoolOfOne(t *testing.T) {
	srv := newCountingServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, 0)

	caller, err := NewOpenAIChatCallerFromConfig(OpenAIConfig{
		Endpoints: []OpenAIEndpoint{{
			Alias: defaultEndpointAlias, APIKey: "test-key", BaseURL: srv.URL,
		}},
	},
		WithRetryPolicy(time.Millisecond, 2),
		WithRecoverTime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewOpenAIChatCaller: %v", err)
	}

	if _, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat)); err == nil {
		t.Fatal("Call succeeded, want failure")
	}

	// One attempt plus two retries, all against the one endpoint.
	if got := srv.hits.Load(); got != 3 {
		t.Errorf("server hits = %d, want 3", got)
	}

	stats := caller.Stats()
	if len(stats) != 1 {
		t.Fatalf("len(Stats()) = %d, want 1", len(stats))
	}

	if stats[0].Alias != defaultEndpointAlias || stats[0].Status != router.StatusDead {
		t.Errorf("stat = %+v, want alias=%q status=dead", stats[0], defaultEndpointAlias)
	}

	// With the endpoint dead and nothing to replace it, the next call fails
	// without touching the network until the recover window elapses.
	before := srv.hits.Load()

	_, err = caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat))
	if !errors.Is(err, ErrNoActiveEndpoints) {
		t.Errorf("second Call error = %v, want ErrNoActiveEndpoints", err)
	}

	if got := srv.hits.Load(); got != before {
		t.Errorf("server was hit %d more times while the endpoint was dead", got-before)
	}
}

// TestComposeCaller_ProbationCostsOneAttempt pins what a recovered endpoint
// costs. The router restores a dead endpoint on the clock alone, which proves
// nothing, so the endpoint comes back on probation: still selectable, but
// attempted once rather than under the retry policy. A pool of one makes the
// difference countable — three attempts to die, one to re-test.
func TestComposeCaller_ProbationCostsOneAttempt(t *testing.T) {
	const recoverTime = 50 * time.Millisecond

	srv := newCountingServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, 0)

	caller, err := NewOpenAIChatCallerFromConfig(OpenAIConfig{
		Endpoints: []OpenAIEndpoint{{
			Alias: defaultEndpointAlias, APIKey: "test-key", BaseURL: srv.URL,
		}},
	},
		WithRetryPolicy(time.Millisecond, 2),
		WithRecoverTime(recoverTime),
	)
	if err != nil {
		t.Fatalf("NewOpenAIChatCaller: %v", err)
	}

	if _, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat)); err == nil {
		t.Fatal("Call succeeded, want failure")
	}

	if got := srv.hits.Load(); got != 3 {
		t.Fatalf("hits before death = %d, want 3 (one attempt plus two retries)", got)
	}

	// Once the window elapses the endpoint reads as on probation rather than
	// as plain available: nothing has confirmed it, only time has passed.
	waitFor(t, func() bool {
		stats := caller.Stats()

		return len(stats) == 1 && stats[0].Status == router.StatusProbation
	})

	before := srv.hits.Load()

	if _, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolOpenAIChat)); err == nil {
		t.Fatal("Call on probation succeeded, want failure")
	}

	if got := srv.hits.Load() - before; got != 1 {
		t.Errorf("hits on probation = %d, want 1 (the retry policy must not apply)", got)
	}

	// A failed probation attempt restarts the window, so the endpoint is dead
	// again rather than staying selectable.
	if stats := caller.Stats(); stats[0].Status != router.StatusDead {
		t.Errorf("status after failed probation = %q, want %q", stats[0].Status, router.StatusDead)
	}
}

// TestSingleEndpointCaller_ClientOptions checks that provider client options
// still reach the client the plain constructor builds.
func TestSingleEndpointCaller_ClientOptions(t *testing.T) {
	var gotHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, anthropicTextReply); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	caller, err := NewAnthropicMessagesCaller(
		"test-key", srv.URL,
		WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")),
		fastRouting(),
	)
	if err != nil {
		t.Fatalf("NewAnthropicMessagesCaller: %v", err)
	}

	if _, err := caller.Call(context.Background(), simpleRequest(schema.ProtocolAnthropicMessages)); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if gotHeader != "context-1m-2025-08-07" {
		t.Errorf("anthropic-beta header = %q, want %q", gotHeader, "context-1m-2025-08-07")
	}
}

// TestComposeCaller_RejectsEmptyEndpoints checks that a misconfiguration
// surfaces at construction rather than on the first call.
func TestComposeCaller_RejectsEmptyEndpoints(t *testing.T) {
	if _, err := NewOpenAIChatCallerFromConfig(OpenAIConfig{}); err == nil {
		t.Error("NewOpenAIChatCallerFromConfig with no endpoints succeeded, want error")
	}

	if _, err := NewAnthropicMessagesCallerFromConfig(AnthropicConfig{}); err == nil {
		t.Error("NewAnthropicMessagesCallerFromConfig with no endpoints succeeded, want error")
	}
}

// TestComposeCallerFromClient_RejectsNil checks the hand-assembled entry
// point's one precondition.
func TestComposeCallerFromClient_RejectsNil(t *testing.T) {
	if _, err := NewOpenAIChatCallerFromBackend(nil); !errors.Is(err, ErrNoBackend) {
		t.Errorf("error = %v, want ErrNoBackend", err)
	}

	if _, err := NewAnthropicMessagesCallerFromBackend(nil); !errors.Is(err, ErrNoBackend) {
		t.Errorf("error = %v, want ErrNoBackend", err)
	}
}

// TestMergeEndpointStats_Probation covers the three-way status merge: the most
// confident opinion any pool holds wins, so probation outranks dead and is in
// turn outranked by a pool that has actually succeeded.
func TestMergeEndpointStats_Probation(t *testing.T) {
	merged := mergeEndpointStats([][]router.EndpointStat{
		{
			{Alias: "a", Status: router.StatusDead},
			{Alias: "b", Status: router.StatusDead},
			{Alias: "c", Status: router.StatusProbation},
		},
		{
			{Alias: "a", Status: router.StatusProbation},
			{Alias: "b", Status: router.StatusDead},
			{Alias: "c", Status: router.StatusAvailable},
		},
	})

	want := []string{router.StatusProbation, router.StatusDead, router.StatusAvailable}
	for i, w := range want {
		if merged[i].Status != w {
			t.Errorf("merged[%d] (%s) status = %q, want %q", i, merged[i].Alias, merged[i].Status, w)
		}
	}
}

// TestMergeEndpointStats_UnknownStatus pins the conservative default: a status
// vage does not recognise is not evidence that a backend is usable, so it
// reports as dead rather than being mistaken for something better.
func TestMergeEndpointStats_UnknownStatus(t *testing.T) {
	merged := mergeEndpointStats([][]router.EndpointStat{
		{{Alias: "a", Status: "quarantined-in-some-future-release"}},
	})

	if merged[0].Status != router.StatusDead {
		t.Errorf("merged status = %q, want %q", merged[0].Status, router.StatusDead)
	}
}

// TestMergeEndpointStats covers the merge rule directly: an endpoint counts as
// dead only when every pool that met it judged it dead, error counts add up,
// and the most recent failure wins.
func TestMergeEndpointStats(t *testing.T) {
	early := time.Now().Add(-time.Minute)
	late := time.Now()

	errEarly := errors.New("early")
	errLate := errors.New("late")

	merged := mergeEndpointStats([][]router.EndpointStat{
		{
			{Alias: "a", Status: router.StatusDead, ErrorCount: 1, LastError: errEarly, ErrorTime: early},
			{Alias: "b", Status: router.StatusAvailable, Active: true},
		},
		{
			{Alias: "a", Status: router.StatusDead, ErrorCount: 2, LastError: errLate, ErrorTime: late},
			{Alias: "b", Status: router.StatusDead, ErrorCount: 1},
		},
	})

	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}

	if merged[0].Alias != "a" || merged[0].Status != router.StatusDead {
		t.Errorf("merged[0] = %+v, want alias=a status=dead", merged[0])
	}

	if merged[0].ErrorCount != 3 {
		t.Errorf("merged[0].ErrorCount = %d, want 3", merged[0].ErrorCount)
	}

	if !errors.Is(merged[0].LastError, errLate) {
		t.Errorf("merged[0].LastError = %v, want %v", merged[0].LastError, errLate)
	}

	// One pool still holds b as available and active, so the caller as a whole
	// has not given up on it.
	if merged[1].Status != router.StatusAvailable || !merged[1].Active {
		t.Errorf("merged[1] = %+v, want status=available active", merged[1])
	}
}
