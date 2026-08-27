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

package taskagent_test

import (
	"context"
	"fmt"
	"log"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// exampleCaller stands in for a real model endpoint so the examples below run
// offline. Swap it for a largemodel.Caller built from your own credentials.
func exampleCaller(answer string) largemodel.Caller {
	return &largemodel.FakeCaller{
		Responses: []*largemodel.Response{
			largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, answer, schema.Usage{}),
		},
	}
}

// ExampleQuick is the shortest way to stand up a TaskAgent: one call carrying
// the identity, the model endpoint, the model name and the system prompt.
func ExampleQuick() {
	a := taskagent.Quick("assistant", "Assistant", exampleCaller("Hello!"), "gpt-4o", "You are helpful.")

	resp, err := agent.RunText(context.Background(), a, "Hi")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(resp.Messages[len(resp.Messages)-1].Text())
	// Output: Hello!
}

// ExampleNew spells out the construction ExampleQuick compresses. The six
// lines here and the one line there build the same agent — Quick is a
// shorthand for the frequent parameters, not a reduced agent — so reach for
// New whenever the agent needs a description, a named prompt template, or any
// other Config field.
func ExampleNew() {
	a := taskagent.New(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithCaller(exampleCaller("Hello!")),
		taskagent.WithModel("gpt-4o"),
		taskagent.WithSystemPrompt(prompt.StringPrompt("You are helpful.")),
	)

	resp, err := agent.RunText(context.Background(), a, "Hi")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(resp.Messages[len(resp.Messages)-1].Text())
	// Output: Hello!
}

// ExampleQuick_withOptions shows that Quick is not a capability subset: any
// remaining Option still layers on after the three presets, and a trailing
// option wins over the matching preset.
func ExampleQuick_withOptions() {
	a := taskagent.Quick(
		"assistant", "Assistant", exampleCaller("Hello!"), "gpt-4o", "You are helpful.",
		taskagent.WithMaxIterations(3),
		taskagent.WithRunTokenBudget(50_000),
	)

	fmt.Println(a.ID(), a.Protocol())
	// Output: assistant openai-chat
}
