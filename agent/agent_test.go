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

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vagent/schema"
)

// TestInterfaceCompliance verifies all agent types satisfy the Agent interface.
func TestInterfaceCompliance(t *testing.T) {
	var _ Agent = (*LLMAgent)(nil)
	var _ Agent = (*WorkflowAgent)(nil)
	var _ Agent = (*RouterAgent)(nil)
	var _ Agent = (*DAGAgent)(nil)
	var _ Agent = (*CustomAgent)(nil)
}

func TestAgentMeta(t *testing.T) {
	m := newAgentMeta(Config{ID: "id-1", Name: "name-1", Description: "desc-1"})
	if m.ID() != "id-1" {
		t.Errorf("ID = %q, want %q", m.ID(), "id-1")
	}
	if m.Name() != "name-1" {
		t.Errorf("Name = %q, want %q", m.Name(), "name-1")
	}
	if m.Description() != "desc-1" {
		t.Errorf("Description = %q, want %q", m.Description(), "desc-1")
	}
}

func TestWorkflowAgent_Config(t *testing.T) {
	a := NewWorkflowAgent(Config{ID: "wf-1", Name: "workflow", Description: "sequential"})
	if a.ID() != "wf-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "wf-1")
	}
	if a.Name() != "workflow" {
		t.Errorf("Name = %q, want %q", a.Name(), "workflow")
	}
	if a.Description() != "sequential" {
		t.Errorf("Description = %q, want %q", a.Description(), "sequential")
	}
}

func TestWorkflowAgent_Run_Stub(t *testing.T) {
	a := NewWorkflowAgent(Config{ID: "wf-1"})
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %q, want 'not yet implemented'", err.Error())
	}
}

func TestRouterAgent_Config(t *testing.T) {
	routes := []Route{
		{Agent: NewCustomAgent(Config{ID: "sub-1"}, nil), Description: "route one"},
	}
	a := NewRouterAgent(Config{ID: "rt-1", Name: "router"}, routes)
	if a.ID() != "rt-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "rt-1")
	}
	if a.Name() != "router" {
		t.Errorf("Name = %q, want %q", a.Name(), "router")
	}
}

func TestRouterAgent_WithRouterFunc(t *testing.T) {
	fn := func(_ context.Context, _ *schema.RunRequest, _ []Route) (Agent, error) {
		return nil, nil
	}
	a := NewRouterAgent(Config{}, nil, WithRouterFunc(fn))
	if a.routerFunc == nil {
		t.Error("routerFunc should not be nil")
	}
}

func TestRouterAgent_Run_Stub(t *testing.T) {
	a := NewRouterAgent(Config{}, nil)
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %q, want 'not yet implemented'", err.Error())
	}
}

func TestDAGAgent_Config(t *testing.T) {
	nodes := []Node{
		{ID: "n1", Agent: NewCustomAgent(Config{ID: "sub-1"}, nil)},
		{ID: "n2", Agent: NewCustomAgent(Config{ID: "sub-2"}, nil), Deps: []string{"n1"}},
	}
	a := NewDAGAgent(Config{ID: "dag-1", Name: "dag"}, nodes)
	if a.ID() != "dag-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "dag-1")
	}
	if a.Name() != "dag" {
		t.Errorf("Name = %q, want %q", a.Name(), "dag")
	}
}

func TestDAGAgent_Run_Stub(t *testing.T) {
	a := NewDAGAgent(Config{ID: "dag-1"}, nil)
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %q, want 'not yet implemented'", err.Error())
	}
}

func TestCustomAgent_Config(t *testing.T) {
	a := NewCustomAgent(Config{ID: "c-1", Name: "custom", Description: "custom agent"}, nil)
	if a.ID() != "c-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "c-1")
	}
	if a.Name() != "custom" {
		t.Errorf("Name = %q, want %q", a.Name(), "custom")
	}
	if a.Description() != "custom agent" {
		t.Errorf("Description = %q, want %q", a.Description(), "custom agent")
	}
}

func TestCustomAgent_Run_Delegates(t *testing.T) {
	fn := func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("echo")},
		}, nil
	}
	a := NewCustomAgent(Config{ID: "c-1"}, fn)
	resp, err := a.Run(context.Background(), &schema.RunRequest{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.Messages[0].Content.Text() != "echo" {
		t.Errorf("response = %q, want %q", resp.Messages[0].Content.Text(), "echo")
	}
}

func TestCustomAgent_Run_NilFunc(t *testing.T) {
	a := NewCustomAgent(Config{ID: "c-1"}, nil)
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error for nil RunFunc")
	}
	if !strings.Contains(err.Error(), "no RunFunc configured") {
		t.Errorf("error = %q, want 'no RunFunc configured'", err.Error())
	}
}
