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

package largemodel_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/vogo/aimodel/composes"
	"github.com/vogo/aimodel/composes/openais"
	"github.com/vogo/aimodel/provider/anthropic"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
)

// ExampleNewOpenAIChatCaller builds a model endpoint on OpenAI's Chat
// Completions API. Passing an empty base URL uses OpenAI's own endpoint; any
// OpenAI-compatible endpoint works by passing its URL instead.
func ExampleNewOpenAIChatCaller() {
	caller, err := largemodel.NewOpenAIChatCaller("sk-your-key", "https://api.openai.com/v1")
	if err != nil {
		log.Fatal(err)
	}

	// Wrap the caller in the governance middlewares. Ordering is meaningful:
	// the first listed is outermost. Retries are not among them — they happen
	// inside the caller's aimodel pool.
	model := largemodel.New(
		caller,
		largemodel.WithMiddleware(
			largemodel.NewTimeoutMiddleware(30*time.Second),
		),
	)

	fmt.Println(model.Protocol())
	// Output: openai-chat
}

// ExampleNewAnthropicMessagesCaller builds a model endpoint on Anthropic's
// Messages API. The differences from OpenAI are the credential, the default
// endpoint, and the vendor options — the call surface is identical.
func ExampleNewAnthropicMessagesCaller() {
	// An empty base URL uses https://api.anthropic.com. Vendor headers are
	// set through the provider's own client options, and the pool's retry and
	// recovery behaviour through aimodel's routing options.
	caller, err := largemodel.NewAnthropicMessagesCaller(
		"sk-ant-your-key", "",
		largemodel.WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")),
		largemodel.WithComposeRouterOptions(composes.WithRetryPolicy(time.Second, 2)),
	)
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(
		caller,
		largemodel.WithMiddleware(
			largemodel.NewLogMiddleware(),
		),
	)

	fmt.Println(model.Protocol())
	// Output: anthropic-messages
}

// ExampleNewOpenAIChatComposeCaller spreads one logical model over several
// OpenAI-compatible endpoints. It differs from NewOpenAIChatCaller only in how
// many endpoints the pool holds: selection, in-call retries and endpoint
// health belong to aimodel's composes either way.
func ExampleNewOpenAIChatComposeCaller() {
	caller, err := largemodel.NewOpenAIChatComposeCaller(
		composes.StrategyFailover,
		[]openais.EndpointSpec{
			{Alias: "primary", BaseURL: "https://api.openai.com/v1", APIKey: "sk-primary", Model: "gpt-4o"},
			{Alias: "backup", BaseURL: "https://backup.example.com/v1", APIKey: "sk-backup", Model: "gpt-4o-mini"},
		},
		largemodel.WithComposeRouterOptions(composes.WithRecoverTime(5*time.Minute)),
		// One pool serves one call at a time, so this is also how many calls
		// the caller serves concurrently before one has to wait.
		largemodel.WithComposeConcurrency(4),
	)
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(
		caller,
		largemodel.WithMiddleware(
			largemodel.NewTimeoutMiddleware(30*time.Second),
		),
	)

	// Endpoint health is readable at any time, merged across the pools.
	for _, stat := range caller.Stats() {
		fmt.Println(stat.Alias, stat.Status)
	}

	fmt.Println(model.Protocol())
	// Output:
	// primary available
	// backup available
	// openai-chat
}

// ExampleCaller_agent wires a caller into a TaskAgent. The agent's protocol
// must match its caller's, because messages are stored in the vendor's own
// wire form and cannot be replayed against a different vendor.
func ExampleCaller_agent() {
	caller, err := largemodel.NewAnthropicMessagesCaller("sk-ant-your-key", "")
	if err != nil {
		log.Fatal(err)
	}

	a := taskagent.New(
		agent.Config{
			ID:       "assistant",
			Name:     "Assistant",
			Protocol: caller.Protocol(),
		},
		taskagent.WithCaller(caller),
		taskagent.WithModel("claude-sonnet-4-5"),
	)

	// Build request messages in the agent's protocol.
	req := &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage(a.Protocol(), "Summarize the release notes."),
		},
	}

	// Run against the live API (skipped here — this example only shows wiring).
	_ = req
	_ = context.Background()

	fmt.Println(a.Protocol())
	// Output: anthropic-messages
}
