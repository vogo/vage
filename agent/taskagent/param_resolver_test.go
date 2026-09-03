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
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

type budgetProbeSource struct {
	onFetch func()
	seen    int
	body    string
}

func (s *budgetProbeSource) Name() string { return "budget_probe" }

func (s *budgetProbeSource) Fetch(_ context.Context, in vctx.FetchInput) (vctx.FetchResult, error) {
	if s.onFetch != nil {
		s.onFetch()
	}
	s.seen = in.Budget
	text := s.body
	if text == "" {
		text = "probe"
	}
	return vctx.FetchResult{
		Messages: []schema.Message{schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleSystem, text)},
		Report: schema.ContextSourceReport{
			Source:  s.Name(),
			Status:  vctx.StatusOK,
			OutputN: 1,
		},
	}, nil
}

func TestWithParamResolver_LastWriteWins(t *testing.T) {
	var first, second atomic.Int64
	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur RunParams) (RunParams, error) {
			first.Add(1)
			return cur, nil
		}),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur RunParams) (RunParams, error) {
			second.Add(1)
			return cur, nil
		}),
	)

	if _, err := a.Run(context.Background(), textRequest("sess-slot")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if first.Load() != 0 {
		t.Errorf("first resolver calls = %d, want 0 (replaced)", first.Load())
	}
	if second.Load() != 1 {
		t.Errorf("second resolver calls = %d, want 1", second.Load())
	}
}

func TestParamResolver_RunsAfterInputGuardsBeforeContext(t *testing.T) {
	var guardCalls, resolverCalls, extraFetches atomic.Int64

	g := guard.NewCustomGuard("order", func(msg *guard.Message) (*guard.Result, error) {
		if msg.Direction == guard.DirectionInput {
			if resolverCalls.Load() != 0 {
				t.Error("resolver ran before input guard")
			}
			guardCalls.Add(1)
		}
		return guard.Pass(), nil
	})

	extra := &budgetProbeSource{onFetch: func() { extraFetches.Add(1) }}

	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithGuards(GuardsConfig{Input: []guard.Guard{g}}),
		WithExtraSources(extra),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur RunParams) (RunParams, error) {
			if extraFetches.Load() != 0 {
				t.Error("context source fetched before resolver")
			}
			if guardCalls.Load() != 1 {
				t.Errorf("guard calls = %d, want 1 before resolver", guardCalls.Load())
			}
			resolverCalls.Add(1)
			return cur, nil
		}),
	)

	if _, err := a.Run(context.Background(), textRequest("sess-order")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resolverCalls.Load() != 1 {
		t.Errorf("resolver calls = %d, want 1", resolverCalls.Load())
	}
	if extraFetches.Load() == 0 {
		t.Error("expected context source to run after resolver")
	}
}

func TestParamResolver_ErrorFailsBeforeModel(t *testing.T) {
	mock := newMock(stopResponse("should-not-run"))
	want := errors.New("deny")
	a := New(
		agent.Config{},
		WithCaller(mock),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, _ RunParams) (RunParams, error) {
			return RunParams{}, want
		}),
	)

	_, err := a.Run(context.Background(), textRequest("sess-fail"))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrapped %v", err, want)
	}
	if n := len(mock.Requests()); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestParamResolver_InvalidToolMode(t *testing.T) {
	mock := newMock(stopResponse("nope"))
	a := New(agent.Config{}, WithCaller(mock))

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		Options:  &schema.RunOptions{ToolMode: "maybe"},
	})
	if !errors.Is(err, ErrInvalidRunParams) {
		t.Fatalf("err = %v, want ErrInvalidRunParams", err)
	}
	if n := len(mock.Requests()); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestLimits_PointerZeroSemantics(t *testing.T) {
	a := New(agent.Config{}, WithRunTokenBudget(10000), WithMaxIterations(8), WithMaxTokens(128))

	zero, three := 0, 3
	p, err := a.mergeRunParams(&schema.RunOptions{
		Limits: &schema.RunLimits{
			RunTokenBudget: &zero,
			MaxTokens:      &zero,
			MaxIterations:  &three,
		},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if p.runTokenBudget != 0 {
		t.Errorf("RunTokenBudget ptr(0) = %d, want 0 (explicit unlimited)", p.runTokenBudget)
	}
	if p.maxTokens != nil {
		t.Errorf("MaxTokens ptr(0) = %v, want nil (omit vendor cap)", p.maxTokens)
	}
	if p.maxIter != 3 {
		t.Errorf("MaxIterations = %d, want 3", p.maxIter)
	}

	_, err = a.mergeRunParams(&schema.RunOptions{
		Limits: &schema.RunLimits{MaxIterations: &zero},
	})
	if !errors.Is(err, ErrInvalidRunParams) {
		t.Errorf("MaxIterations ptr(0) err = %v, want ErrInvalidRunParams", err)
	}
}

func TestEventParamsResolved_CredscrubAndTiming(t *testing.T) {
	var events []schema.Event
	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		events = append(events, e)
		return nil
	}))

	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	a := New(
		agent.Config{ID: "obs"},
		WithCaller(newMock(stopResponse("ok"))),
		WithHookManager(mgr),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur RunParams) (RunParams, error) {
			cur.Subject = "tenant-" + secret
			return cur, nil
		}),
	)

	if _, err := a.Run(context.Background(), textRequest("sess-obs")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var resolved *schema.ParamsResolvedData
	resolvedIdx := -1
	contextIdx := -1
	for i, e := range events {
		switch e.Type {
		case schema.EventParamsResolved:
			data, ok := e.Data.(schema.ParamsResolvedData)
			if !ok {
				t.Fatalf("payload type = %T", e.Data)
			}
			resolved = &data
			resolvedIdx = i
		case schema.EventContextBuilt:
			contextIdx = i
		}
	}
	if resolved == nil {
		t.Fatal("missing EventParamsResolved")
	}
	if resolvedIdx < 0 || contextIdx < 0 || resolvedIdx >= contextIdx {
		t.Errorf("params_resolved index %d, context_built index %d", resolvedIdx, contextIdx)
	}
	if !resolved.ResolverTouched {
		t.Error("resolver_touched = false, want true")
	}
	if strings.Contains(resolved.Subject, secret) {
		t.Errorf("subject still contains credential: %q", resolved.Subject)
	}
	if resolved.ToolsSHA256 == "" {
		t.Error("tools_sha256 is empty")
	}
}

func TestEventParamsResolved_ResolverFailureOmitsSuccessEvent(t *testing.T) {
	var saw bool
	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		if e.Type == schema.EventParamsResolved {
			saw = true
		}
		return nil
	}))

	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithHookManager(mgr),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, _ RunParams) (RunParams, error) {
			return RunParams{}, errors.New("nope")
		}),
	)
	if _, err := a.Run(context.Background(), textRequest("sess-no-event")); err == nil {
		t.Fatal("expected resolver error")
	}
	if saw {
		t.Error("EventParamsResolved must not fire on resolver failure")
	}
}

func TestPrepareAITools_ToolModeNone(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "echo"}, echoToolHandler)

	a := New(agent.Config{}, WithCaller(newMock(stopResponse("ok"))), WithToolRegistry(reg))
	got := a.prepareFrozenAITools(nil)
	if got != nil {
		t.Errorf("frozen empty = %v, want nil", got)
	}
}

func TestBuildInputBudget_NonZeroTrimsOptional(t *testing.T) {
	src := &budgetProbeSource{body: strings.Repeat("x", 64)} // 16 tokens
	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithExtraSources(src),
	)

	budget := 8
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		Options: &schema.RunOptions{
			Limits: &schema.RunLimits{RunTokenBudget: &budget},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.seen == 0 {
		t.Error("optional source Budget = 0, want remaining positive budget")
	}
}

func TestBuildInputBudget_ZeroIsUnlimited(t *testing.T) {
	src := &budgetProbeSource{body: strings.Repeat("x", 64)}
	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithExtraSources(src),
		WithRunTokenBudget(0),
	)
	if _, err := a.Run(context.Background(), textRequest("sess-unlim")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.seen != 0 {
		t.Errorf("optional source Budget = %d, want 0 (unlimited)", src.seen)
	}
}
