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

package taskagent

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const entryPolicyProbeKey = "entry-policy/conformance"

// entryCrossCutObservations records what each run-class entry actually did.
type entryCrossCutObservations struct {
	middlewareCalls atomic.Int64
	inputGuardCalls atomic.Int64
	runValuesBound  atomic.Bool
}

func (o *entryCrossCutObservations) middleware() agent.Middleware {
	return agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			o.middlewareCalls.Add(1)
			if schema.SetRunValue(ctx, entryPolicyProbeKey, true) {
				o.runValuesBound.Store(true)
			}

			return next(ctx, req)
		}
	})
}

func (o *entryCrossCutObservations) inputGuardSpy() guard.Guard {
	return guard.NewCustomGuard("entry-policy-spy", func(msg *guard.Message) (*guard.Result, error) {
		if msg.Direction == guard.DirectionInput {
			o.inputGuardCalls.Add(1)
		}

		return guard.Pass(), nil
	})
}

func (o *entryCrossCutObservations) runValuesHook() *hook.Manager {
	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(ctx context.Context, e schema.Event) error {
		if e.Type == schema.EventAgentStart && schema.SetRunValue(ctx, entryPolicyProbeKey, true) {
			o.runValuesBound.Store(true)
		}

		return nil
	}))

	return mgr
}

func newEntryPolicyAgent(o *entryCrossCutObservations, opts ...Option) *Agent {
	base := []Option{
		WithCaller(streamingMock()),
		WithGuards(GuardsConfig{Input: []guard.Guard{o.inputGuardSpy()}}),
		WithMiddleware(o.middleware()),
		WithHookManager(o.runValuesHook()),
	}

	return New(agent.Config{ID: "entry-policy"}, append(base, opts...)...)
}

func askUserRegistry() tool.ToolRegistry {
	r := tool.NewRegistry()
	_ = r.Register(
		schema.ToolDef{Name: "ask_user", Description: "ask a human"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "SHOULD NEVER RUN"), nil
		},
	)

	return r
}

func TestEntryPolicy_ConstantsMatchMatrix(t *testing.T) {
	tests := []struct {
		name   string
		policy entryPolicy
		want   entryPolicy
	}{
		{"fresh run", policyFreshRun, entryPolicy{initRunValues: true, inputGuards: true, agentMiddleware: true}},
		{"resume", policyResume, entryPolicy{initRunValues: true, inputGuards: false, agentMiddleware: false}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.policy != tt.want {
				t.Errorf("policy = %+v, want %+v", tt.policy, tt.want)
			}
		})
	}
}

// TestEntryPolicy_Conformance asserts each run-class entry honors its
// entryPolicy for run values, input guards, and agent middleware.
func TestEntryPolicy_Conformance(t *testing.T) {
	type want struct {
		middlewareCalls int64
		inputGuardCalls int64
		runValuesBound  bool
	}

	tests := []struct {
		name string
		run  func(t *testing.T, obs *entryCrossCutObservations) error
		want want
	}{
		{
			name: "Run",
			run: func(t *testing.T, obs *entryCrossCutObservations) error {
				t.Helper()

				a := newEntryPolicyAgent(obs)
				_, err := a.Run(context.Background(), textRequest("sess-entry-run"))

				return err
			},
			want: want{middlewareCalls: 1, inputGuardCalls: 1, runValuesBound: true},
		},
		{
			name: "RunStream",
			run: func(t *testing.T, obs *entryCrossCutObservations) error {
				t.Helper()

				a := newEntryPolicyAgent(obs)
				rs, err := a.RunStream(context.Background(), textRequest("sess-entry-stream"))
				if err != nil {
					return err
				}

				for {
					if _, err := rs.Recv(); err != nil {
						if err == io.EOF {
							return nil
						}

						return err
					}
				}
			},
			want: want{middlewareCalls: 1, inputGuardCalls: 1, runValuesBound: true},
		},
		{
			name: "Resume",
			run: func(t *testing.T, obs *entryCrossCutObservations) error {
				t.Helper()

				store := checkpoint.NewMapIterationStore()
				first := New(
					agent.Config{ID: "entry-policy"},
					WithCaller(newMock(toolCallResponse("tc-1", "echo", `{"v":"a"}`))),
					WithToolRegistry(newEchoRegistry()),
					WithIterationStore(store),
				)
				if _, err := first.Run(context.Background(), textRequest("sess-entry-resume")); err == nil {
					t.Fatal("expected the scripted caller to run out of responses")
				}

				a := newEntryPolicyAgent(
					obs,
					WithCaller(newMock(stopResponse("resumed"))),
					WithIterationStore(store),
				)

				_, err := a.Resume(context.Background(), "sess-entry-resume")

				return err
			},
			want: want{middlewareCalls: 0, inputGuardCalls: 0, runValuesBound: true},
		},
		{
			name: "ResumeInterrupt",
			run: func(t *testing.T, obs *entryCrossCutObservations) error {
				t.Helper()

				store := interrupt.NewMapStore()
				suspend := New(
					agent.Config{ID: "entry-policy"},
					WithCaller(newMock(toolCallResponse("tc-1", "ask_user", `{"question":"proceed?"}`))),
					WithToolRegistry(askUserRegistry()),
					WithInterruptStore(store),
					WithInterruptToolNames("ask_user"),
				)

				first, err := suspend.Run(context.Background(), textRequest("sess-entry-interrupt"))
				if err != nil {
					return err
				}
				if first.Interrupt == nil {
					t.Fatal("expected interrupt descriptor from suspend run")
				}

				a := newEntryPolicyAgent(
					obs,
					WithCaller(newMock(stopResponse("done"))),
					WithToolRegistry(askUserRegistry()),
					WithInterruptStore(store),
					WithInterruptToolNames("ask_user"),
				)

				_, err = a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
					InterruptID: first.Interrupt.InterruptID,
					Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "approved"}},
				})

				return err
			},
			want: want{middlewareCalls: 0, inputGuardCalls: 0, runValuesBound: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obs entryCrossCutObservations

			if err := tt.run(t, &obs); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}

			if got := obs.middlewareCalls.Load(); got != tt.want.middlewareCalls {
				t.Errorf("middleware calls = %d, want %d", got, tt.want.middlewareCalls)
			}
			if got := obs.inputGuardCalls.Load(); got != tt.want.inputGuardCalls {
				t.Errorf("input guard calls = %d, want %d", got, tt.want.inputGuardCalls)
			}
			if got := obs.runValuesBound.Load(); got != tt.want.runValuesBound {
				t.Errorf("run values bound = %v, want %v", got, tt.want.runValuesBound)
			}
		})
	}
}
