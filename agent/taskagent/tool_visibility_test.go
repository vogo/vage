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
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/skill"
	"github.com/vogo/vage/tool"
)

func twoToolRegistry() tool.ToolRegistry {
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "echo"}, echoToolHandler)
	_ = reg.Register(schema.ToolDef{Name: "search"}, echoToolHandler)
	return reg
}

func toolNamesOnWire(t *testing.T, a *Agent, opts *schema.RunOptions) []string {
	t.Helper()
	mock := newMock(stopResponse("ok"))
	a.caller = mock
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		SessionID: "sess-tools",
		Options:   opts,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mock.Requests()) == 0 {
		t.Fatal("no model request")
	}
	var names []string
	for _, d := range mock.Requests()[0].Tools {
		names = append(names, d.Name)
	}
	return names
}

func TestToolVisibility_ModeNoneIsEmpty(t *testing.T) {
	a := New(agent.Config{}, WithCaller(newMock()), WithToolRegistry(twoToolRegistry()))
	got := toolNamesOnWire(t, a, &schema.RunOptions{ToolMode: schema.ToolModeNone})
	if len(got) != 0 {
		t.Errorf("tools = %v, want empty", got)
	}
}

func TestToolVisibility_AllowEmptyIsEmpty(t *testing.T) {
	a := New(agent.Config{}, WithCaller(newMock()), WithToolRegistry(twoToolRegistry()))
	got := toolNamesOnWire(t, a, &schema.RunOptions{ToolMode: schema.ToolModeAllow, Tools: nil})
	if len(got) != 0 {
		t.Errorf("allow empty = %v, want empty (no FilterTools fallback)", got)
	}
}

func TestToolVisibility_CompatEmptyIsUnrestricted(t *testing.T) {
	a := New(agent.Config{}, WithCaller(newMock()), WithToolRegistry(twoToolRegistry()))
	got := toolNamesOnWire(t, a, nil)
	if len(got) != 2 {
		t.Errorf("compat unrestricted = %v, want both tools", got)
	}
}

func TestToolVisibility_RequestAndSkillIntersect(t *testing.T) {
	reg := skill.NewRegistry()
	_ = reg.Register(&skill.Def{Name: "s", Description: "d", AllowedTools: []string{"echo"}})
	mgr := skill.NewManager(reg)
	if _, err := mgr.Activate(context.Background(), "s", "sess-tools"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	a := New(
		agent.Config{},
		WithCaller(newMock()),
		WithToolRegistry(twoToolRegistry()),
		WithSkillManager(mgr),
	)
	got := toolNamesOnWire(t, a, &schema.RunOptions{Tools: []string{"echo", "search"}})
	if len(got) != 1 || got[0] != "echo" {
		t.Errorf("intersect = %v, want [echo]", got)
	}
}

func TestToolVisibility_EnabledFuncErrorExcludesAndEmits(t *testing.T) {
	var events []schema.Event
	hm := hook.NewManager()
	hm.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		events = append(events, e)
		return nil
	}))

	secret := "sk-abcdefghijklmnopqrstuvwxyz012345"
	a := New(
		agent.Config{},
		WithCaller(newMock()),
		WithToolRegistry(twoToolRegistry()),
		WithHookManager(hm),
		WithParamResolver(func(_ context.Context, _ *schema.RunRequest, cur RunParams) (RunParams, error) {
			cur.EnabledFunc = func(_ context.Context, name string) (bool, error) {
				if name == "search" {
					return false, errors.New("denied " + secret)
				}
				return true, nil
			}
			return cur, nil
		}),
	)

	got := toolNamesOnWire(t, a, nil)
	if len(got) != 1 || got[0] != "echo" {
		t.Errorf("after EnabledFunc = %v, want [echo]", got)
	}

	var excluded bool
	for _, e := range events {
		if e.Type != schema.EventToolExcluded {
			continue
		}
		excluded = true
		data := e.Data.(schema.ToolExcludedData)
		if data.ToolName != "search" {
			t.Errorf("excluded name = %q", data.ToolName)
		}
		if strings.Contains(data.Reason, secret) {
			t.Errorf("reason leaked credential: %q", data.Reason)
		}
	}
	if !excluded {
		t.Error("missing EventToolExcluded")
	}
}

func TestToolVisibility_FrozenIgnoresLaterSkill(t *testing.T) {
	skillReg := skill.NewRegistry()
	_ = skillReg.Register(&skill.Def{Name: "wide", Description: "d", AllowedTools: []string{"echo", "search"}})
	mgr := skill.NewManager(skillReg)
	if _, err := mgr.Activate(context.Background(), "wide", "sess-tools"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	a := New(
		agent.Config{},
		WithCaller(newMock(stopResponse("ok"))),
		WithToolRegistry(twoToolRegistry()),
		WithSkillManager(mgr),
	)
	got := a.toolsForRun(runParams{toolFilter: []string{"echo"}, toolsFrozen: true}, "sess-tools")
	if len(got) != 1 || got[0].Name != "echo" {
		t.Errorf("frozen tools = %v, want [echo] (skill must not re-expand)", toolDefNames(got))
	}
}

func toolDefNames(defs []schema.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}
