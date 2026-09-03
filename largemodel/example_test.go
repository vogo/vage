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

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/largemodel/middleware"
	"github.com/vogo/vage/schema"
)

// ExampleNewCaller builds a caller over a single OpenAI endpoint. The type of
// the argument picks the protocol, and an unnamed lone endpoint is aliased
// "default". Passing an empty base URL uses OpenAI's own endpoint; any
// OpenAI-compatible endpoint works by passing its URL instead.
func ExampleNewCaller() {
	caller, err := largemodel.NewCaller(largemodel.OpenAIEndpoint{
		APIKey: "sk-your-key", BaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Wrap the caller in the governance middlewares. Ordering is meaningful:
	// the first listed is outermost. Retries are not among them — they happen
	// inside the caller's router pool.
	model := largemodel.New(
		caller,
		largemodel.WithMiddleware(
			middleware.NewTimeoutMiddleware(30*time.Second),
		),
	)

	fmt.Println(model.Protocol())
	// Output: openai-chat
}

// ExampleNewCaller_anthropic builds a model endpoint on Anthropic's Messages
// API. Only the endpoint type differs from the OpenAI example — that type is
// what selects the wire protocol, at compile time.
func ExampleNewCaller_anthropic() {
	// An empty base URL uses https://api.anthropic.com. Vendor headers are
	// set through the provider's own client options, and the pool's retry and
	// recovery behaviour through largemodel routing options.
	caller, err := largemodel.NewCaller(
		largemodel.AnthropicEndpoint{
			APIKey:  "sk-ant-your-key",
			BaseURL: "",
		},
		largemodel.WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")),
		largemodel.WithRetryPolicy(time.Second, 2),
	)
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(
		caller,
		largemodel.WithMiddleware(
			middleware.NewLogMiddleware(),
		),
	)

	fmt.Println(model.Protocol())
	// Output: anthropic-messages
}

// ExampleBuildCaller spreads one logical model over several
// OpenAI-compatible endpoints.
func ExampleBuildCaller() {
	caller, err := largemodel.BuildCaller(
		largemodel.OpenAIConfig{
			Strategy: largemodel.StrategyFailover,
			Endpoints: []largemodel.OpenAIEndpoint{
				{Alias: "primary", BaseURL: "https://api.openai.com/v1", APIKey: "sk-primary", Model: "gpt-4o"},
				{Alias: "backup", BaseURL: "https://backup.example.com/v1", APIKey: "sk-backup", Model: "gpt-4o-mini"},
			},
		},
		largemodel.WithRecoverTime(5*time.Minute),
		largemodel.WithConcurrency(4),
	)
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(caller)

	a := taskagent.New(
		agent.Config{ID: "assistant", Protocol: model.Protocol()},
		taskagent.WithCaller(model),
		taskagent.WithModel("gpt-4o"),
	)

	fmt.Println(a.Protocol())
	// Output: openai-chat
}

// ExampleBuildCaller_tenantBinding maps a tenant (or credential domain) onto
// a same-protocol ComposeCaller at Agent construction. Failover stays inside
// that caller's router pool; the ReAct loop does not swap callers.
func ExampleBuildCaller_tenantBinding() {
	type tenantID string

	buildForTenant := func(_ tenantID) (largemodel.ComposeCaller, error) {
		return largemodel.BuildCaller(largemodel.OpenAIConfig{
			Strategy: largemodel.StrategyFailover,
			Endpoints: []largemodel.OpenAIEndpoint{
				{Alias: "primary", BaseURL: "https://api.openai.com/v1", APIKey: "sk-primary", Model: "gpt-4o"},
				{Alias: "backup", BaseURL: "https://backup.example.com/v1", APIKey: "sk-backup", Model: "gpt-4o-mini"},
			},
		})
	}

	// Host cache: one ComposeCaller per tenant, reused across Runs.
	callers := map[tenantID]largemodel.Caller{}
	c, err := buildForTenant("acme")
	if err != nil {
		log.Fatal(err)
	}
	callers["acme"] = c

	model := largemodel.New(callers["acme"])
	a := taskagent.New(
		agent.Config{ID: "assistant", Protocol: model.Protocol()},
		taskagent.WithCaller(model),
		taskagent.WithModel("gpt-4o"),
	)

	fmt.Println(a.Protocol())
	// Output: openai-chat
}

// ExampleBuildCaller_endpointStats shows per-endpoint health.
func ExampleBuildCaller_endpointStats() {
	caller, err := largemodel.BuildCaller(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{
			{Alias: "only", APIKey: "sk-test", BaseURL: "https://api.openai.com/v1"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	stats := caller.EndpointStats()
	if len(stats) == 1 {
		fmt.Println(stats[0].Alias, stats[0].Status)
	}
	// Output: only available
}

// ExampleWrapCaller wraps a client the caller built itself. Nothing is routed,
// retried or health-tracked around it, so the result is a plain Caller with no
// endpoint health to report. The protocol follows from which backend methods
// the client implements.
func ExampleWrapCaller() {
	caller, err := largemodel.WrapCaller(openai.NewClient("sk-your-key"))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(caller.Protocol())
	// Output: openai-chat
}

// ExampleCaller_protocolMismatch shows that messages must match the caller's
// protocol.
func ExampleCaller_protocolMismatch() {
	caller, err := largemodel.BuildCaller(largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: "sk-test"}},
	})
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(caller)
	foreign := schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi")
	_, err = model.Call(context.Background(), &largemodel.Request{
		Messages: []schema.Message{
			foreign,
		},
	})
	fmt.Println(err != nil)
	// Output: true
}
