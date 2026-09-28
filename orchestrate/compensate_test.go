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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/largemodel/schema"
)

// compensatableRunner is a test runner that implements Compensatable.
type compensatableRunner struct {
	runFn        func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
	compensateFn func(ctx context.Context, original *schema.RunResponse) error
	idempotent   bool
}

func (c *compensatableRunner) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return c.runFn(ctx, req)
}

func (c *compensatableRunner) Compensate(ctx context.Context, original *schema.RunResponse) error {
	return c.compensateFn(ctx, original)
}

func (c *compensatableRunner) Idempotent() bool {
	return c.idempotent
}

func TestCompensate_BackwardCompensation(t *testing.T) {
	var compensationOrder []string
	compensationMu := make(chan struct{}, 1)

	newCompRunner := func(id string) *compensatableRunner {
		return &compensatableRunner{
			runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				return &schema.RunResponse{Messages: req.Messages}, nil
			},
			compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
				compensationMu <- struct{}{}
				compensationOrder = append(compensationOrder, id)
				<-compensationMu
				return nil
			},
			idempotent: true,
		}
	}

	nodes := []Node{
		{ID: "A", Runner: newCompRunner("A")},
		{ID: "B", Runner: newCompRunner("B"), Deps: []string{"A"}},
		{ID: "C", Runner: errorRunner(errors.New("C failed")), Deps: []string{"B"}},
	}

	cfg := DAGConfig{
		ErrorStrategy: Compensate,
		CompensateCfg: &CompensateConfig{
			Strategy: BackwardCompensate,
			Timeout:  5 * time.Second,
		},
	}

	result, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err == nil {
		t.Fatal("expected error from failing node C")
	}

	// A and B should have been compensated in reverse order (B first, then A).
	if len(compensationOrder) != 2 {
		t.Fatalf("expected 2 compensations, got %d: %v", len(compensationOrder), compensationOrder)
	}
	if compensationOrder[0] != "B" || compensationOrder[1] != "A" {
		t.Errorf("expected compensation order [B, A], got %v", compensationOrder)
	}

	if result.NodeStatus["A"] != NodeCompensated {
		t.Errorf("A status = %d, want NodeCompensated", result.NodeStatus["A"])
	}
	if result.NodeStatus["B"] != NodeCompensated {
		t.Errorf("B status = %d, want NodeCompensated", result.NodeStatus["B"])
	}
}

func TestCompensate_ForwardRecovery(t *testing.T) {
	var callCount atomic.Int32

	retryRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		count := callCount.Add(1)
		if count <= 2 {
			return nil, errors.New("transient error")
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []Node{
		{ID: "A", Runner: passthroughRunner()},
		{ID: "B", Runner: retryRunner, Deps: []string{"A"}},
	}

	cfg := DAGConfig{
		ErrorStrategy: Compensate,
		CompensateCfg: &CompensateConfig{
			Strategy:   ForwardRecovery,
			MaxRetries: 5,
			Timeout:    5 * time.Second,
		},
	}

	result, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NodeStatus["B"] != NodeDone {
		t.Errorf("B status = %d, want NodeDone", result.NodeStatus["B"])
	}
}

func TestCompensate_ForwardRecovery_ExhaustedRetries(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: passthroughRunner()},
		{ID: "B", Runner: errorRunner(errors.New("permanent error")), Deps: []string{"A"}},
	}

	cfg := DAGConfig{
		ErrorStrategy: Compensate,
		CompensateCfg: &CompensateConfig{
			Strategy:   ForwardRecovery,
			MaxRetries: 1,
			Timeout:    1 * time.Second,
		},
	}

	_, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
}

func TestCompensatable_Interface(t *testing.T) {
	runner := &compensatableRunner{
		runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages}, nil
		},
		compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
			return nil
		},
		idempotent: true,
	}

	// Verify it implements both interfaces.
	var _ Runner = runner
	var _ Compensatable = runner

	if !runner.Idempotent() {
		t.Error("expected idempotent = true")
	}
}

func TestTopologicalSort(t *testing.T) {
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"A"}},
		{ID: "D", Deps: []string{"B", "C"}},
	}
	order := topologicalSort(nodes)

	// A should come first, D should come last.
	if len(order) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(order))
	}
	if order[0] != "A" {
		t.Errorf("first should be A, got %q", order[0])
	}
	if order[3] != "D" {
		t.Errorf("last should be D, got %q", order[3])
	}
}
