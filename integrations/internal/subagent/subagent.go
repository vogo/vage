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

// Package subagent builds the real TaskAgents the parent-layer integration
// tests need: one that suspends for a human decision and one that finishes
// normally. The suspending agent is a genuine TaskAgent with a real
// interrupt.Store and InterruptPolicy — not a stub returning a hand-written
// StopReason — so the tests exercise the same response a production
// human-in-the-loop agent produces.
package subagent

import (
	"context"
	"sync/atomic"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// Protocol is the wire form these agents are scripted against.
const Protocol = schema.ProtocolOpenAIChat

// HalfWrittenText is the assistant text the suspending agent emits alongside
// the frozen tool call. Parent layers must never surface it as an answer, so
// tests assert on its absence.
const HalfWrittenText = "I am about to ask a human"

// AskUserTool is the tool name the suspending agent's InterruptPolicy flags.
const AskUserTool = "ask_user"

// Suspending returns a TaskAgent whose only scripted turn requests a flagged
// tool call, so its first Run freezes the batch and returns
// StopReasonInterrupted with a persisted record. handlerRuns, when non-nil,
// counts executions of the flagged tool handler — it must stay at zero.
func Suspending(id string, handlerRuns *atomic.Int32) *taskagent.Agent {
	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: AskUserTool, Description: "ask a human"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			if handlerRuns != nil {
				handlerRuns.Add(1)
			}

			return schema.TextResult("", "SHOULD NEVER RUN"), nil
		},
	)

	caller := &largemodel.FakeCaller{Responses: []*largemodel.Response{{
		Message: schema.NewAssistantTurn(Protocol, HalfWrittenText, "", []schema.ToolCall{{
			ID:        "tc-1",
			Name:      AskUserTool,
			Arguments: `{"question":"proceed?"}`,
		}}),
		FinishReason: largemodel.FinishReasonToolCalls,
		Usage:        schema.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
	}}}

	return taskagent.New(
		agent.Config{ID: id, Name: id, Description: "suspends for a human decision"},
		taskagent.WithCaller(caller),
		taskagent.WithToolRegistry(reg),
		taskagent.WithInterruptStore(interrupt.NewMapStore()),
		taskagent.WithInterruptToolNames(AskUserTool),
	)
}

// Completing returns a TaskAgent that answers with text in one turn, for the
// control half of every test: rejecting suspensions must not disturb it.
func Completing(id, text string) *taskagent.Agent {
	caller := &largemodel.FakeCaller{Responses: []*largemodel.Response{{
		Message:      schema.NewAssistantTurn(Protocol, text, "", nil),
		FinishReason: largemodel.FinishReasonStop,
		Usage:        schema.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
	}}}

	return taskagent.New(
		agent.Config{ID: id, Name: id, Description: "answers directly"},
		taskagent.WithCaller(caller),
	)
}

// SessionScoped wraps ag so that a parent which builds its own RunRequest
// without a session id (agent-as-tool does) still reaches a TaskAgent able to
// persist an interrupt record — interrupt.Store rejects an empty session id.
// It is plumbing only: the wrapped TaskAgent still produces the suspended
// response under test.
func SessionScoped(ag agent.Agent, sessionID string) agent.Agent {
	return agent.NewCustomAgent(
		agent.Config{ID: ag.ID(), Name: ag.Name(), Description: ag.Description(), Protocol: ag.Protocol()},
		func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			if req.SessionID == "" {
				scoped := *req
				scoped.SessionID = sessionID
				req = &scoped
			}

			return ag.Run(ctx, req)
		},
	)
}

// Request builds a one-user-message RunRequest for these agents.
func Request(sessionID, text string) *schema.RunRequest {
	return &schema.RunRequest{
		SessionID: sessionID,
		Messages:  []schema.Message{schema.NewUserMessage(Protocol, text)},
	}
}
