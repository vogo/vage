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
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// =============================================================================
// Internal function tests (require package-level access)
// =============================================================================

func TestRunRunnerWithTimeout_Success(t *testing.T) {
	runner := passthroughRunner()
	resp, err := runRunnerWithTimeout(context.Background(), 0, runner, makeReq("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Messages[0].Text() != "hello" {
		t.Errorf("got %q, want %q", resp.Messages[0].Text(), "hello")
	}
}

func TestRunRunnerWithTimeout_Timeout(t *testing.T) {
	slowRunner := newMockRunner(func(ctx context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &schema.RunResponse{}, nil
		}
	})
	_, err := runRunnerWithTimeout(context.Background(), 50*time.Millisecond, slowRunner, makeReq("hello"))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRunRunnerWithTimeout_ZeroTimeout(t *testing.T) {
	runner := passthroughRunner()
	resp, err := runRunnerWithTimeout(context.Background(), 0, runner, makeReq("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Messages[0].Text() != "hello" {
		t.Errorf("got %q, want %q", resp.Messages[0].Text(), "hello")
	}
}

func TestBuildDAGMaps(t *testing.T) {
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"A"}},
		{ID: "D", Deps: []string{"B", "C"}},
	}

	downstream, inDegree := buildDAGMaps(nodes)

	if len(downstream["A"]) != 2 {
		t.Errorf("A downstream = %v, want 2 entries", downstream["A"])
	}
	if len(downstream["D"]) != 0 {
		t.Errorf("D downstream = %v, want empty", downstream["D"])
	}
	if inDegree["A"] != 0 {
		t.Errorf("A in-degree = %d, want 0", inDegree["A"])
	}
	if inDegree["D"] != 2 {
		t.Errorf("D in-degree = %d, want 2", inDegree["D"])
	}
}

func TestTopologicalSort_Linear(t *testing.T) {
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"B"}},
	}
	order := topologicalSort(nodes)
	if len(order) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(order))
	}
	if order[0] != "A" || order[1] != "B" || order[2] != "C" {
		t.Errorf("got %v, want [A B C]", order)
	}
}

func TestTopologicalSort_Diamond(t *testing.T) {
	nodes := []Node{
		{ID: "A"},
		{ID: "B", Deps: []string{"A"}},
		{ID: "C", Deps: []string{"A"}},
		{ID: "D", Deps: []string{"B", "C"}},
	}
	order := topologicalSort(nodes)
	if len(order) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(order))
	}
	if order[0] != "A" {
		t.Errorf("first should be A, got %s", order[0])
	}
	if order[3] != "D" {
		t.Errorf("last should be D, got %s", order[3])
	}
}

func TestTokenBucket_PreciseWait(t *testing.T) {
	tb := newTokenBucket(10.0)
	ctx := context.Background()

	for i := range 10 {
		if err := tb.wait(ctx); err != nil {
			t.Fatalf("burst wait %d failed: %v", i, err)
		}
	}

	start := time.Now()
	if err := tb.wait(ctx); err != nil {
		t.Fatalf("wait after burst failed: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 50*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Errorf("wait took %v, expected ~100ms", elapsed)
	}
}

func TestTokenBucket_ContextCancel(t *testing.T) {
	tb := newTokenBucket(1.0)
	ctx := context.Background()

	if err := tb.wait(ctx); err != nil {
		t.Fatalf("first wait failed: %v", err)
	}

	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()

	err := tb.wait(cancelCtx)
	if err == nil {
		t.Fatal("expected context error")
	}
}
