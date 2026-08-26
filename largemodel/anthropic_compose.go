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

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/provider/anthropics"
	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/vage/schema"
)

// AnthropicMessagesComposeCaller is a Caller over one or more
// Anthropic-compatible endpoints, and like its OpenAI counterpart it is what
// every Anthropic Messages caller vage builds actually is. The division of
// labour is the same: routing, retries and endpoint health belong to
// largemodel/router, including the reading of a 400 as retryable.
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
func (c *AnthropicMessagesComposeCaller) Stats() []EndpointStat {
	return mergeEndpointStats(c.pool.snapshot(func(cc *anthropics.ComposeClient) []router.EndpointStat {
		return cc.Stats()
	}))
}

// EndpointStats reports endpoint health merged across the caller's pools.
func (c *AnthropicMessagesComposeCaller) EndpointStats() []EndpointStat { return c.Stats() }

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
	_ Caller                   = (*AnthropicMessagesComposeCaller)(nil)
	_ AnthropicMessagesBackend = (*anthropicComposeBackend)(nil)
)
