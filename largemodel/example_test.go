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
	// inside the caller's router pool.
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
	// recovery behaviour through largemodel routing options.
	caller, err := largemodel.NewAnthropicMessagesCaller(
		"sk-ant-your-key", "",
		largemodel.WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")),
		largemodel.WithRetryPolicy(time.Second, 2),
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

// ExampleNewOpenAIChatCallerFromConfig spreads one logical model over several
// OpenAI-compatible endpoints.
func ExampleNewOpenAIChatCallerFromConfig() {
	caller, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
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

// ExampleNewOpenAIChatCallerFromConfig_endpointStats shows per-endpoint health.
func ExampleNewOpenAIChatCallerFromConfig_endpointStats() {
	caller, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
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

// ExampleCaller_protocolMismatch shows that messages must match the caller's
// protocol.
func ExampleCaller_protocolMismatch() {
	caller, err := largemodel.NewOpenAIChatCaller("sk-test", "")
	if err != nil {
		log.Fatal(err)
	}

	model := largemodel.New(caller)
	_, err = model.Call(context.Background(), &largemodel.Request{
		Messages: []schema.Message{
			schema.NewAnthropicMessage(anthropic.MessagesMessage{Role: "user", Content: []byte(`"hi"`)}, "hi"),
		},
	})
	fmt.Println(err != nil)
	// Output: true
}
