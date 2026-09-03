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
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/workspace"
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

// ExampleWithInterrupt shows the grouped interrupt configuration: the
// persistence store and the tool-name policy that pauses ask_user are read
// as one reviewable unit instead of four flat options.
func ExampleWithInterrupt() {
	_, err := taskagent.NewValidated(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithInterrupt(taskagent.InterruptConfig{
			Store:     interrupt.NewMapStore(),
			ToolNames: []string{"ask_user"},
		}),
	)
	fmt.Println(err)
	// Output: <nil>
}

// ExampleWithInterrupt_policy shows the grouped form with a custom
// InterruptPolicy in place of the tool-name shortcut.
func ExampleWithInterrupt_policy() {
	_, err := taskagent.NewValidated(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithInterrupt(taskagent.InterruptConfig{
			Store: interrupt.NewMapStore(),
			Policy: taskagent.InterruptPolicyFunc(func(_ context.Context, _ string, calls []schema.ToolCall) []string {
				// Flag every call in the batch for this demonstration.
				ids := make([]string, 0, len(calls))
				for _, c := range calls {
					ids = append(ids, c.ID)
				}
				return ids
			}),
		}),
	)
	fmt.Println(err)
	// Output: <nil>
}

// ExampleNewValidated demonstrates the validated constructor: a broken
// interrupt pair — here a store without a policy — fails at assembly time,
// before any model, storage or tool I/O, instead of at the first Run.
func ExampleNewValidated() {
	_, err := taskagent.NewValidated(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithInterrupt(taskagent.InterruptConfig{Store: interrupt.NewMapStore()}),
	)
	fmt.Println("config error:", errors.Is(err, taskagent.ErrInterruptConfig))
	// Output: config error: true
}

// ExampleWithGuards shows the grouped guard configuration: the three
// execution-position lists read as one unit.
func ExampleWithGuards() {
	_, err := taskagent.NewValidated(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithGuards(taskagent.GuardsConfig{
			Input: []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 1000})},
		}),
	)
	fmt.Println(err)
	// Output: <nil>
}

// ExampleWithParamResolver installs the single construction-time slot that
// runs after input guards and before tools are frozen. Resume does not call
// it. Subject is an opaque audit string, not an auth principal.
func ExampleWithParamResolver() {
	a := taskagent.New(
		agent.Config{ID: "assistant", Name: "Assistant"},
		taskagent.WithCaller(exampleCaller("ok")),
		taskagent.WithModel("gpt-4o"),
		taskagent.WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur taskagent.RunParams) (taskagent.RunParams, error) {
			cur.Subject = "tenant-acme"
			cur.ToolMode = schema.ToolModeNone
			return cur, nil
		}),
	)

	resp, err := agent.RunText(context.Background(), a, "Hi")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(resp.Messages[len(resp.Messages)-1].Text())
	// Output: ok
}

// ExampleWithContextBudget_deepAgent wires workspace, unified window budget,
// on-demand summarization and EventContextBuilt observation — the Deep Agent
// composition without a separate recipe package.
func ExampleWithContextBudget_deepAgent() {
	dir, err := os.MkdirTemp("", "vage-ws")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ws, err := workspace.NewFileWorkspace(dir)
	if err != nil {
		log.Fatal(err)
	}
	plan := strings.Repeat("H", 90) + strings.Repeat("T", 10)
	if err := ws.WritePlan(context.Background(), "deep-session", plan); err != nil {
		log.Fatal(err)
	}

	sess := memory.NewSessionMemory("deep", "deep-session")
	summarizer := func(_ context.Context, older []schema.Message) (string, error) {
		return fmt.Sprintf("summarized %d messages", len(older)), nil
	}
	mgr := memory.NewManager(
		memory.WithSession(sess),
		memory.WithCompressor(memory.SummarizeWhenOverBudget(
			summarizer,
			memory.KeepRecentTurns(2),
			memory.TargetUtilization(0.8),
		)),
	)

	caller := &largemodel.FakeCaller{
		Responses: []*largemodel.Response{
			largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, "done", schema.Usage{}),
		},
		Chunks: []*largemodel.Chunk{
			{TextDelta: "done", FinishReason: largemodel.FinishReasonStop},
		},
	}

	a := taskagent.New(
		agent.Config{ID: "deep", Name: "Deep"},
		taskagent.WithCaller(caller),
		taskagent.WithSystemPrompt(prompt.StringPrompt("You are a deep agent.")),
		taskagent.WithMemory(mgr),
		taskagent.WithContextBudget(memory.Budget{ModelContextTokens: 8_000, ReservedOutput: 512}),
		taskagent.WithExtraSources(&vctx.WorkspaceSource{Workspace: ws, MaxBytes: 10}),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "deep-session",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "continue the plan")},
	})
	if err != nil {
		log.Fatal(err)
	}

	var types []string
	for {
		ev, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			log.Fatal(recvErr)
		}
		types = append(types, ev.Type)
		if ev.Type != schema.EventContextBuilt {
			continue
		}
		data := ev.Data.(schema.ContextBuiltData)
		fmt.Println("available_history", data.AvailableHistory > 0)
		for _, src := range data.Sources {
			if src.Source == vctx.SourceNameWorkspace {
				fmt.Println("workspace", src.Status, strings.Contains(src.Note, vctx.NoteWorkspaceTailKeep))
			}
		}
	}
	fmt.Println(types[0], types[1])
	// Output:
	// available_history true
	// workspace truncated true
	// agent_start context_built
}
