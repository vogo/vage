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
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/schema"
)

func TestCheckInterruptConfig(t *testing.T) {
	tests := []struct {
		name    string
		store   interrupt.Store
		policy  InterruptPolicy
		wantErr bool
	}{
		{"neither configured", nil, nil, false},
		{"both configured", interrupt.NewMapStore(), InterruptPolicyFunc(func(context.Context, string, []schema.ToolCall) []string { return nil }), false},
		{"store only", interrupt.NewMapStore(), nil, true},
		{"policy only", nil, InterruptPolicyFunc(func(context.Context, string, []schema.ToolCall) []string { return nil }), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{interruptStore: tt.store, interruptPolicy: tt.policy}
			err := a.checkInterruptConfig()
			if tt.wantErr && !errors.Is(err, ErrInterruptConfig) {
				t.Errorf("checkInterruptConfig() = %v, want ErrInterruptConfig", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("checkInterruptConfig() = %v, want nil", err)
			}
		})
	}
}

func TestAgent_Run_InterruptConfig_OnlyStoreConfigured(t *testing.T) {
	a := New(agent.Config{}, WithCaller(newMock(stopResponse("hi"))), WithInterruptStore(interrupt.NewMapStore()))
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
	})
	if !errors.Is(err, ErrInterruptConfig) {
		t.Errorf("Run() err = %v, want ErrInterruptConfig", err)
	}
}

func TestAgent_Run_InterruptConfig_OnlyPolicyConfigured(t *testing.T) {
	a := New(agent.Config{}, WithCaller(newMock(stopResponse("hi"))), WithInterruptToolNames("ask_user"))
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
	})
	if !errors.Is(err, ErrInterruptConfig) {
		t.Errorf("Run() err = %v, want ErrInterruptConfig", err)
	}
}

func TestInterruptDescriptorFromRecord_PreservesOriginalOrder(t *testing.T) {
	rec := &interrupt.Record{
		ToolCalls: []schema.ToolCall{
			{ID: "a", Name: "echo"},
			{ID: "b", Name: "ask_user"},
			{ID: "c", Name: "ask_user"},
		},
		Pending: []string{"c", "b"}, // deliberately out of ToolCalls order
	}

	desc := interruptDescriptorFromRecord(rec)
	if len(desc.Pending) != 2 {
		t.Fatalf("Pending len = %d, want 2", len(desc.Pending))
	}
	if desc.Pending[0].ID != "b" || desc.Pending[1].ID != "c" {
		t.Errorf("Pending order = [%s, %s], want [b, c] (ToolCalls order)", desc.Pending[0].ID, desc.Pending[1].ID)
	}
}

func TestEffectiveParamsRoundTrip(t *testing.T) {
	temp := 0.5
	maxTok := 256
	p := runParams{
		model:          "gpt-x",
		temperature:    &temp,
		maxIter:        7,
		runTokenBudget: 1000,
		maxTokens:      &maxTok,
		toolFilter:     []string{"a", "b"},
		stopSeq:        []string{"STOP"},
	}

	ep := runParamsToEffective(p)
	back := effectiveParamsToRunParams(ep)

	if back.model != p.model || back.maxIter != p.maxIter || back.runTokenBudget != p.runTokenBudget {
		t.Errorf("round trip mismatch: %+v vs %+v", back, p)
	}
	if *back.temperature != temp || *back.maxTokens != maxTok {
		t.Errorf("pointer fields not round-tripped: temp=%v maxTok=%v", back.temperature, back.maxTokens)
	}
	if len(back.toolFilter) != 2 || len(back.stopSeq) != 1 {
		t.Errorf("slice fields not round-tripped: %+v", back)
	}
}

func TestInterruptPolicyByToolName(t *testing.T) {
	p := interruptPolicyByToolName{"ask_user": {}}
	calls := []schema.ToolCall{
		{ID: "1", Name: "echo"},
		{ID: "2", Name: "ask_user"},
	}
	pending := p.Intercept(context.Background(), "sess", calls)
	if len(pending) != 1 || pending[0] != "2" {
		t.Errorf("pending = %v, want [2]", pending)
	}
}
