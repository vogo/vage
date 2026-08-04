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
	"sync"

	"github.com/vogo/aimodel/composes"
	"github.com/vogo/aimodel/composes/anthropics"
	"github.com/vogo/aimodel/composes/openais"
	"github.com/vogo/aimodel/provider/anthropic"
	"github.com/vogo/aimodel/provider/openai"

	"github.com/vogo/vage/schema"
)

// defaultComposeConcurrency is how many pools a compose caller builds at most.
// An aimodel pool serves one call at a time, so this is also the number of
// model calls the caller can have in flight before one has to wait.
const defaultComposeConcurrency = 8

// ComposeOption configures a compose caller.
type ComposeOption func(*composeConfig)

type composeConfig struct {
	routerOpts  []composes.Option
	concurrency int

	// Provider client options reach the single-endpoint constructors, which
	// own the client they build. The declaratively built pools take their
	// connection details from their endpoint specs instead, and ignore these.
	openAIClientOpts    []openai.ClientOption
	anthropicClientOpts []anthropic.ClientOption
}

// WithComposeRouterOptions passes aimodel's own routing options through to
// every pool the caller builds — retry policy, recover time, attempt
// observers. They describe the pool's operational behaviour, which on this
// path belongs to aimodel rather than to vage's middlewares.
//
// A caller may dispatch through several pools concurrently. Consequently, an
// attempt observer registered with [composes.WithAttemptObserver] may be called
// concurrently and must synchronize access to any shared state itself.
func WithComposeRouterOptions(opts ...composes.Option) ComposeOption {
	return func(c *composeConfig) {
		c.routerOpts = append(c.routerOpts, opts...)
	}
}

// WithComposeConcurrency caps how many pools the caller builds, and thus how
// many model calls it serves concurrently. A call arriving when every pool is
// busy waits for one to free up rather than failing. Zero or negative selects
// the default.
//
// Pools are built lazily, so a caller that is never used concurrently only
// ever holds one.
func WithComposeConcurrency(n int) ComposeOption {
	return func(c *composeConfig) {
		c.concurrency = n
	}
}

// WithOpenAIClientOptions passes provider client options — base URL overrides,
// timeouts, custom headers — to the client [NewOpenAIChatCaller] builds. It has
// no effect on a pool built from endpoint specs, which carries its connection
// details per endpoint.
func WithOpenAIClientOptions(opts ...openai.ClientOption) ComposeOption {
	return func(c *composeConfig) {
		c.openAIClientOpts = append(c.openAIClientOpts, opts...)
	}
}

// WithAnthropicClientOptions is the Anthropic counterpart of
// [WithOpenAIClientOptions], for the client [NewAnthropicMessagesCaller]
// builds.
func WithAnthropicClientOptions(opts ...anthropic.ClientOption) ComposeOption {
	return func(c *composeConfig) {
		c.anthropicClientOpts = append(c.anthropicClientOpts, opts...)
	}
}

func newComposeConfig(opts ...ComposeOption) *composeConfig {
	cfg := &composeConfig{concurrency: defaultComposeConcurrency}

	for _, o := range opts {
		o(cfg)
	}

	if cfg.concurrency <= 0 {
		cfg.concurrency = defaultComposeConcurrency
	}

	return cfg
}

// composePool hands out aimodel compose clients one caller at a time.
//
// An aimodel pool belongs to one conversation and serves it one call at a
// time: a second concurrent call is rejected with composes.ErrCallInProgress
// rather than queued. vage shares a Caller across agents that do run in
// parallel, so the caller keeps several pools and lends one out per call,
// which is aimodel's own prescription — one pool per concurrent worker.
//
// Each pool learns endpoint health independently. That is the price of
// parallelism here: a backend that dies is discovered once per pool that
// meets it, not once for the caller as a whole.
type composePool[T any] struct {
	// idle holds the pools not currently serving a call. Its capacity is the
	// concurrency limit, so a release never blocks.
	idle chan T

	// mu guards clients, which is both the record of every pool built so far
	// and the count that decides whether another may be built.
	mu      sync.Mutex
	clients []T

	limit int
	build func() (T, error)
}

// newComposePool builds a pool set of at most limit members. One member is
// built immediately so a misconfiguration — an empty endpoint list, a
// duplicate alias, a recover time shorter than the retry backoff — surfaces
// here rather than on the first call.
func newComposePool[T any](limit int, build func() (T, error)) (*composePool[T], error) {
	first, err := build()
	if err != nil {
		return nil, err
	}

	p := &composePool[T]{
		idle:    make(chan T, limit),
		clients: []T{first},
		limit:   limit,
		build:   build,
	}
	p.idle <- first

	return p, nil
}

// acquire borrows a pool, building a new one while the limit allows and
// otherwise waiting for a busy one to be released. A cancelled context ends
// the wait.
func (p *composePool[T]) acquire(ctx context.Context) (T, error) {
	var zero T

	select {
	case c := <-p.idle:
		return c, nil
	default:
	}

	p.mu.Lock()

	// Look again under the lock: a pool released between the check above and
	// this point should be reused rather than answered with a new one. Without
	// this, ordinary contention grows the set to its limit even when the
	// existing pools would have sufficed — and every extra pool is one more
	// place that has to discover a dead endpoint for itself.
	select {
	case c := <-p.idle:
		p.mu.Unlock()

		return c, nil
	default:
	}

	if len(p.clients) < p.limit {
		c, err := p.build()
		if err != nil {
			p.mu.Unlock()

			return zero, err
		}

		p.clients = append(p.clients, c)
		p.mu.Unlock()

		return c, nil
	}

	p.mu.Unlock()

	select {
	case c := <-p.idle:
		return c, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// release returns a pool to the idle set. The channel's capacity is the limit
// and only borrowed pools are released, so the send never blocks.
func (p *composePool[T]) release(c T) {
	select {
	case p.idle <- c:
	default:
	}
}

// snapshot collects one health snapshot per pool built so far. aimodel allows
// Stats to run concurrently with dispatch, so a borrowed pool is included.
func (p *composePool[T]) snapshot(stats func(T) []composes.EndpointStat) [][]composes.EndpointStat {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([][]composes.EndpointStat, 0, len(p.clients))
	for _, c := range p.clients {
		out = append(out, stats(c))
	}

	return out
}

// mergeEndpointStats folds the per-pool snapshots into one view keyed by
// alias, in the order aliases first appear.
//
// The merge states what the caller as a whole knows about a backend. Its error
// count is the sum across pools and its last error is the most recent one any
// pool recorded; Active means at least one pool is currently serving from it.
// The status is the most confident opinion any pool holds, in the order
// available > probation > dead: one pool having succeeded against a backend is
// what the caller knows about it, whatever the pools that have not tried since
// still believe. A merged "dead" therefore means every pool agrees, and a
// merged "probation" that the best any of them can say is that a recover
// window elapsed.
func mergeEndpointStats(snapshots [][]composes.EndpointStat) []composes.EndpointStat {
	var (
		order  []string
		merged = map[string]*composes.EndpointStat{}
		best   = map[string]int{}
	)

	for _, snapshot := range snapshots {
		for i := range snapshot {
			stat := snapshot[i]

			acc, ok := merged[stat.Alias]
			if !ok {
				copied := stat
				copied.ErrorCount = 0
				merged[stat.Alias] = &copied
				order = append(order, stat.Alias)
				acc = &copied
				best[stat.Alias] = statusConfidence(stat.Status)
			}

			best[stat.Alias] = max(best[stat.Alias], statusConfidence(stat.Status))

			acc.ErrorCount += stat.ErrorCount
			acc.Active = acc.Active || stat.Active

			if stat.ErrorTime.After(acc.ErrorTime) {
				acc.LastError = stat.LastError
				acc.ErrorTime = stat.ErrorTime
			}
		}
	}

	out := make([]composes.EndpointStat, 0, len(order))

	for _, alias := range order {
		acc := merged[alias]
		acc.Status = statusOfConfidence(best[alias])

		out = append(out, *acc)
	}

	return out
}

// Status confidence levels, ordered from what the pools know least to what
// they know best. aimodel's status set has grown once already, so an unknown
// value sorts at the bottom rather than being mistaken for a known one.
const (
	confidenceUnknown = iota
	confidenceDead
	confidenceProbation
	confidenceAvailable
)

// statusConfidence ranks one pool's opinion of an endpoint.
func statusConfidence(status string) int {
	switch status {
	case composes.StatusAvailable:
		return confidenceAvailable
	case composes.StatusProbation:
		return confidenceProbation
	case composes.StatusDead:
		return confidenceDead
	default:
		return confidenceUnknown
	}
}

// statusOfConfidence names the merged rank. An unknown rank reports as dead:
// a status vage cannot read is not evidence that a backend is usable.
func statusOfConfidence(rank int) string {
	switch rank {
	case confidenceAvailable:
		return composes.StatusAvailable
	case confidenceProbation:
		return composes.StatusProbation
	default:
		return composes.StatusDead
	}
}

// defaultEndpointAlias names the single endpoint of a one-endpoint pool, so
// its health snapshots and error attribution read the same as a pool's.
const defaultEndpointAlias = "default"

// OpenAIChatComposeCaller is a Caller over one or more OpenAI-compatible
// endpoints. Every OpenAI Chat Completions caller vage builds is one of these:
// a single endpoint is a pool of one, so the reliability story does not change
// shape when a second endpoint is added.
//
// Routing, in-call retries and endpoint health are aimodel's: a pool retries
// its active endpoint with exponential waits (500ms doubling, three retries by
// default), marks it dead when they are exhausted, and fails over to the next
// candidate the strategy names — or, with nothing to fail over to, returns
// composes.ErrNoActiveModels until the recover window (60s by default) elapses.
// [WithComposeRouterOptions] tunes all three.
//
// A recovered endpoint comes back on probation rather than restored: the clock
// proves nothing on its own, so the next real call re-tests it with a single
// attempt instead of a whole retry round, and only a success promotes it. That
// is what Stats reports as composes.StatusProbation.
//
// One consequence deserves stating plainly: aimodel judges only HTTP 401 and
// 403 as unretryable, so a deterministic 400 is retried like a transient
// failure and then costs the endpoint its recover window. [IsRetryable] is
// vage's own, narrower reading of the same error, for callers deciding whether
// a failure is worth reacting to.
type OpenAIChatComposeCaller struct {
	caller Caller
	pool   *composePool[*openais.ComposeClient]
}

// Protocol implements Caller.
func (c *OpenAIChatComposeCaller) Protocol() schema.Protocol { return c.caller.Protocol() }

// Call implements Caller.
func (c *OpenAIChatComposeCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	return c.caller.Call(ctx, req)
}

// CallStream implements Caller.
func (c *OpenAIChatComposeCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return c.caller.CallStream(ctx, req)
}

// NewOpenAIChatComposeCaller builds a Caller over several OpenAI-compatible
// endpoints, routed by the given strategy.
//
// Each spec names its own model, which replaces the model on the request when
// that endpoint serves it: a vage Request states the model it wants, and an
// endpoint that speaks of the same model under another name overrides it.
func NewOpenAIChatComposeCaller(
	strategy composes.Strategy, specs []openais.EndpointSpec, opts ...ComposeOption,
) (*OpenAIChatComposeCaller, error) {
	cfg := newComposeConfig(opts...)

	return newOpenAIComposeCaller(func() (*openais.ComposeClient, error) {
		return openais.NewFromEndpoints(strategy, specs, cfg.routerOpts...)
	}, cfg)
}

// newOpenAIComposeCaller wires a pool set built by build behind the OpenAI
// caller. It is what both entry points — one endpoint and several — end up
// calling, so the two differ only in how a pool is built.
func newOpenAIComposeCaller(
	build func() (*openais.ComposeClient, error), cfg *composeConfig,
) (*OpenAIChatComposeCaller, error) {
	pool, err := newComposePool(cfg.concurrency, build)
	if err != nil {
		return nil, err
	}

	return &OpenAIChatComposeCaller{
		caller: &openAIChatCaller{client: &openAIComposeBackend{pool: pool}},
		pool:   pool,
	}, nil
}

// NewOpenAIChatCallerFromBackend wraps a backend the caller built itself — a
// bare *openai.Client, a hand-assembled compose client, a test double. The
// backend is used as-is: nothing is routed, retried or health-tracked around
// it, and if it is an aimodel pool it still serves one call at a time and must
// not be shared with agents that run in parallel.
func NewOpenAIChatCallerFromBackend(backend OpenAIChatBackend) (Caller, error) {
	if backend == nil {
		return nil, ErrNoBackend
	}

	return &openAIChatCaller{client: backend}, nil
}

// Stats reports endpoint health merged across the caller's pools. See
// mergeEndpointStats for what merging means when pools disagree.
func (c *OpenAIChatComposeCaller) Stats() []composes.EndpointStat {
	return mergeEndpointStats(c.pool.snapshot(func(cc *openais.ComposeClient) []composes.EndpointStat {
		return cc.Stats()
	}))
}

// openAIComposeBackend borrows a pool for the duration of one call.
type openAIComposeBackend struct {
	pool *composePool[*openais.ComposeClient]
}

// ChatCompletions implements OpenAIChatBackend.
func (b *openAIComposeBackend) ChatCompletions(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionResponse, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.ChatCompletions(ctx, request)
}

// ChatCompletionsStream implements OpenAIChatBackend. The pool is released as
// soon as the stream is established: aimodel's routing covers establishment
// only, and the stream that follows no longer passes through it.
func (b *openAIComposeBackend) ChatCompletionsStream(
	ctx context.Context, request *openai.ChatCompletionRequest,
) (*openai.ChatCompletionStream, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.ChatCompletionsStream(ctx, request)
}

// AnthropicMessagesComposeCaller is a Caller over one or more
// Anthropic-compatible endpoints, and like its OpenAI counterpart it is what
// every Anthropic Messages caller vage builds actually is. The division of
// labour is the same: routing, retries and endpoint health belong to aimodel,
// including the reading of a 400 as retryable.
type AnthropicMessagesComposeCaller struct {
	caller Caller
	pool   *composePool[*anthropics.ComposeClient]
}

// Protocol implements Caller.
func (c *AnthropicMessagesComposeCaller) Protocol() schema.Protocol { return c.caller.Protocol() }

// Call implements Caller.
func (c *AnthropicMessagesComposeCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	return c.caller.Call(ctx, req)
}

// CallStream implements Caller.
func (c *AnthropicMessagesComposeCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	return c.caller.CallStream(ctx, req)
}

// NewAnthropicMessagesComposeCaller builds a Caller over several
// Anthropic-compatible endpoints, routed by the given strategy.
func NewAnthropicMessagesComposeCaller(
	strategy composes.Strategy, specs []anthropics.EndpointSpec, opts ...ComposeOption,
) (*AnthropicMessagesComposeCaller, error) {
	cfg := newComposeConfig(opts...)

	return newAnthropicComposeCaller(func() (*anthropics.ComposeClient, error) {
		return anthropics.NewFromEndpoints(strategy, specs, cfg.routerOpts...)
	}, cfg)
}

// newAnthropicComposeCaller wires a pool set built by build behind the
// Anthropic caller, for both the one-endpoint and the several-endpoint path.
func newAnthropicComposeCaller(
	build func() (*anthropics.ComposeClient, error), cfg *composeConfig,
) (*AnthropicMessagesComposeCaller, error) {
	pool, err := newComposePool(cfg.concurrency, build)
	if err != nil {
		return nil, err
	}

	return &AnthropicMessagesComposeCaller{
		caller: &anthropicMessagesCaller{client: &anthropicComposeBackend{pool: pool}},
		pool:   pool,
	}, nil
}

// NewAnthropicMessagesCallerFromBackend wraps a backend the caller built
// itself. As on the OpenAI side, nothing is routed, retried or health-tracked
// around it.
func NewAnthropicMessagesCallerFromBackend(backend AnthropicMessagesBackend) (Caller, error) {
	if backend == nil {
		return nil, ErrNoBackend
	}

	return &anthropicMessagesCaller{client: backend}, nil
}

// Stats reports endpoint health merged across the caller's pools.
func (c *AnthropicMessagesComposeCaller) Stats() []composes.EndpointStat {
	return mergeEndpointStats(c.pool.snapshot(func(cc *anthropics.ComposeClient) []composes.EndpointStat {
		return cc.Stats()
	}))
}

// anthropicComposeBackend borrows a pool for the duration of one call.
type anthropicComposeBackend struct {
	pool *composePool[*anthropics.ComposeClient]
}

// Messages implements AnthropicMessagesBackend.
func (b *anthropicComposeBackend) Messages(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessagesResponse, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.Messages(ctx, request)
}

// MessagesStream implements AnthropicMessagesBackend. The pool is released as
// soon as the stream is established.
func (b *anthropicComposeBackend) MessagesStream(
	ctx context.Context, request *anthropic.MessagesRequest,
) (*anthropic.MessageStream, error) {
	client, err := b.pool.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer b.pool.release(client)

	return client.MessagesStream(ctx, request)
}

var (
	_ Caller                   = (*OpenAIChatComposeCaller)(nil)
	_ Caller                   = (*AnthropicMessagesComposeCaller)(nil)
	_ OpenAIChatBackend        = (*openAIComposeBackend)(nil)
	_ AnthropicMessagesBackend = (*anthropicComposeBackend)(nil)
)
