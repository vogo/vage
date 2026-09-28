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

package orchestrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vogo/largemodel/schema"
)

func TestPriorityQueue_BasicOrdering(t *testing.T) {
	pq := newPriorityQueue()
	pq.push("low", 1)
	pq.push("high", 10)
	pq.push("mid", 5)

	// Should come out in priority order: high, mid, low.
	if id := pq.pop(); id != "high" {
		t.Errorf("expected high, got %q", id)
	}
	if id := pq.pop(); id != "mid" {
		t.Errorf("expected mid, got %q", id)
	}
	if id := pq.pop(); id != "low" {
		t.Errorf("expected low, got %q", id)
	}
}

func TestPriorityQueue_FIFO_SamePriority(t *testing.T) {
	pq := newPriorityQueue()
	pq.push("first", 5)
	pq.push("second", 5)
	pq.push("third", 5)

	// Same priority: FIFO order.
	if id := pq.pop(); id != "first" {
		t.Errorf("expected first, got %q", id)
	}
	if id := pq.pop(); id != "second" {
		t.Errorf("expected second, got %q", id)
	}
	if id := pq.pop(); id != "third" {
		t.Errorf("expected third, got %q", id)
	}
}

func TestPriorityQueue_Empty(t *testing.T) {
	pq := newPriorityQueue()
	if !pq.empty() {
		t.Error("new queue should be empty")
	}
	pq.push("a", 1)
	if pq.empty() {
		t.Error("queue should not be empty after push")
	}
	pq.pop()
	if !pq.empty() {
		t.Error("queue should be empty after pop")
	}
}

func TestComputeCriticalPath_Linear(t *testing.T) {
	// A -> B -> C (critical path is the only path)
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"B"}},
	}
	priorities := ComputeCriticalPath(nodes)

	// All nodes on critical path should have the same (maximum) priority.
	if priorities["A"] != priorities["B"] || priorities["B"] != priorities["C"] {
		t.Errorf("linear chain: all priorities should be equal, got A=%d B=%d C=%d",
			priorities["A"], priorities["B"], priorities["C"])
	}
}

func TestComputeCriticalPath_Diamond(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D
	// Critical path: A -> B -> D (or A -> C -> D), all have slack 0.
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"A"}},
		{ID: "D", Deps: []string{"B", "C"}},
	}
	priorities := ComputeCriticalPath(nodes)

	// All nodes are on the critical path in a diamond.
	if priorities["A"] <= 0 {
		t.Errorf("A priority should be positive, got %d", priorities["A"])
	}
	if priorities["D"] <= 0 {
		t.Errorf("D priority should be positive, got %d", priorities["D"])
	}
}

func TestComputeCriticalPath_WithShortBranch(t *testing.T) {
	// A -> B -> C -> D (critical path)
	// A -> E -> D (short branch, E has slack)
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"B"}},
		{ID: "E", Deps: []string{"A"}},
		{ID: "D", Deps: []string{"C", "E"}},
	}
	priorities := ComputeCriticalPath(nodes)

	// E has slack, so its priority should be lower than B/C.
	if priorities["E"] >= priorities["B"] {
		t.Errorf("E (slack) priority %d should be less than B (critical) priority %d",
			priorities["E"], priorities["B"])
	}
}

func TestDAG_PriorityScheduling(t *testing.T) {
	var mu sync.Mutex
	var executionOrder []string

	makeTrackerRunner := func(id string) *mockRunner {
		return newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			mu.Lock()
			executionOrder = append(executionOrder, id)
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			return &schema.RunResponse{Messages: req.Messages}, nil
		})
	}

	// All depend on root, so they're all ready at the same time.
	// MaxConcurrency=1 forces serial execution, so priority matters.
	nodes := []Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "low", Runner: makeTrackerRunner("low"), Deps: []string{"root"}, Priority: 1},
		{ID: "high", Runner: makeTrackerRunner("high"), Deps: []string{"root"}, Priority: 10},
		{ID: "mid", Runner: makeTrackerRunner("mid"), Deps: []string{"root"}, Priority: 5},
	}

	cfg := DAGConfig{
		MaxConcurrency:     1,
		PriorityScheduling: true,
	}
	_, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With MaxConcurrency=1 and priority scheduling, order should be: high, mid, low.
	if len(executionOrder) != 3 {
		t.Fatalf("expected 3 executions, got %d: %v", len(executionOrder), executionOrder)
	}
	if executionOrder[0] != "high" {
		t.Errorf("first should be high, got %q (order: %v)", executionOrder[0], executionOrder)
	}
	if executionOrder[1] != "mid" {
		t.Errorf("second should be mid, got %q (order: %v)", executionOrder[1], executionOrder)
	}
	if executionOrder[2] != "low" {
		t.Errorf("third should be low, got %q (order: %v)", executionOrder[2], executionOrder)
	}
}

func TestDAG_CriticalPathAuto(t *testing.T) {
	// A -> B -> C -> D (critical path)
	// A -> E -> D (short branch)
	// With CriticalPathAuto, B and C should get higher priority than E.
	nodes := []Node{
		{ID: "A", Runner: passthroughRunner()},
		{ID: "B", Runner: passthroughRunner(), Deps: []string{"A"}},
		{ID: "C", Runner: passthroughRunner(), Deps: []string{"B"}},
		{ID: "E", Runner: passthroughRunner(), Deps: []string{"A"}},
		{ID: "D", Runner: passthroughRunner(), Deps: []string{"C", "E"}},
	}

	cfg := DAGConfig{
		PriorityScheduling: true,
		CriticalPathAuto:   true,
	}
	result, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All nodes should complete.
	for _, n := range nodes {
		if result.NodeStatus[n.ID] != NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", n.ID, result.NodeStatus[n.ID])
		}
	}
}
