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

package service

import (
	"context"
	"testing"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

func TestServiceRegisterAgent(t *testing.T) {
	svc := New(Config{Addr: ":0"})

	a := agent.NewCustomAgent(agent.Config{
		ID:   "test-agent",
		Name: "Test Agent",
	}, func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{}, nil
	})

	svc.RegisterAgent(a)

	got, ok := svc.getAgent("test-agent")
	if !ok {
		t.Fatal("expected to find agent")
	}

	if got.ID() != "test-agent" {
		t.Fatalf("expected agent ID %q, got %q", "test-agent", got.ID())
	}
}

func TestServiceGetAgentNotFound(t *testing.T) {
	svc := New(Config{Addr: ":0"})

	_, ok := svc.getAgent("nonexistent")
	if ok {
		t.Fatal("expected agent not found")
	}
}

func TestServiceListAgentsSorted(t *testing.T) {
	svc := New(Config{Addr: ":0"})

	for _, id := range []string{"charlie", "alpha", "bravo"} {
		svc.RegisterAgent(agent.NewCustomAgent(agent.Config{
			ID:   id,
			Name: id,
		}, func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		}))
	}

	agents := svc.listAgentsSorted()

	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}

	expected := []string{"alpha", "bravo", "charlie"}
	for i, a := range agents {
		if a.ID() != expected[i] {
			t.Fatalf("expected agent[%d] ID %q, got %q", i, expected[i], a.ID())
		}
	}
}
