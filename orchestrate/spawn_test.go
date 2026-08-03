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
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

func TestDynamicSpawnNode_Basic(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1", Runner: passthroughRunner()},
		Spawner: func(_ context.Context, output *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "child-A", Runner: appendRunner("-A")},
				{ID: "child-B", Runner: appendRunner("-B")},
			}, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ConcatMessagesAggregator concatenates by sorted node ID.
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}
	texts := []string{resp.Messages[0].Text(), resp.Messages[1].Text()}
	// child-A and child-B sorted alphabetically.
	if texts[0] != "start-A" || texts[1] != "start-B" {
		t.Errorf("got %v, want [start-A, start-B]", texts)
	}
}

func TestDynamicSpawnNode_NilSpawner(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1"},
	}
	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected error for nil Spawner")
	}
}

func TestDynamicSpawnNode_MaxSpawnCount(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node:          Node{ID: "spawn1", Runner: passthroughRunner()},
		MaxSpawnCount: 2,
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "child-1", Runner: passthroughRunner()},
				{ID: "child-2", Runner: passthroughRunner()},
				{ID: "child-3", Runner: passthroughRunner()},
			}, nil
		},
	}

	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected error for exceeding MaxSpawnCount")
	}
	if !strings.Contains(err.Error(), "MaxSpawnCount") {
		t.Errorf("error should mention MaxSpawnCount: %v", err)
	}
}

func TestDynamicSpawnNode_SpawnDepthLimit(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node:            Node{ID: "spawn1", Runner: passthroughRunner()},
		SpawnDepthLimit: 1,
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return nil, nil
		},
	}

	// First level should succeed.
	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate depth exceeded via context.
	ctx := context.WithValue(context.Background(), spawnDepthKey{}, 1)
	_, err = ExecuteDynamicSpawn(ctx, dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected error for exceeding SpawnDepthLimit")
	}
	if !strings.Contains(err.Error(), "SpawnDepthLimit") {
		t.Errorf("error should mention SpawnDepthLimit: %v", err)
	}
}

func TestDynamicSpawnNode_SpawnTimeout(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node:         Node{ID: "spawn1", Runner: passthroughRunner()},
		SpawnTimeout: 50 * time.Millisecond,
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "slow", Runner: newMockRunner(func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(5 * time.Second):
						return &schema.RunResponse{Messages: req.Messages}, nil
					}
				})},
			}, nil
		},
	}

	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDynamicSpawnNode_ChildError(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1", Runner: passthroughRunner()},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "bad-child", Runner: errorRunner(errors.New("child failed"))},
			}, nil
		},
	}

	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected error from child")
	}
	if !strings.Contains(err.Error(), "child failed") {
		t.Errorf("error should mention child failure: %v", err)
	}
}

func TestDynamicSpawnNode_EmptySpawn(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1", Runner: appendRunner("-parent")},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return nil, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-parent" {
		t.Errorf("got %q, want %q", got, "start-parent")
	}
}

func TestDynamicSpawnNode_CustomAggregator(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node:            Node{ID: "spawn1", Runner: passthroughRunner()},
		SpawnAggregator: LastResultAggregator(),
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "child-A", Runner: appendRunner("-A")},
				{ID: "child-B", Runner: appendRunner("-B")},
			}, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LastResultAggregator picks last by sorted ID -> child-B.
	got := resp.Messages[0].Text()
	if got != "start-B" {
		t.Errorf("got %q, want %q", got, "start-B")
	}
}

func TestDynamicSpawnNode_ChildError_CancelsOthers(t *testing.T) {
	slowCancelled := make(chan struct{})
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1", Runner: passthroughRunner()},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "fast-fail", Runner: errorRunner(errors.New("fast fail"))},
				{ID: "slow", Runner: newMockRunner(func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
					select {
					case <-ctx.Done():
						close(slowCancelled)
						return nil, ctx.Err()
					case <-time.After(10 * time.Second):
						return &schema.RunResponse{Messages: req.Messages}, nil
					}
				})},
			}, nil
		},
	}

	_, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err == nil {
		t.Fatal("expected error from child")
	}

	// The slow child should have its context cancelled.
	select {
	case <-slowCancelled:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("slow child was not cancelled after sibling failure")
	}
}

func TestDynamicSpawnNode_NilRunner(t *testing.T) {
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "spawn1"},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{
				{ID: "child-A", Runner: appendRunner("-A")},
			}, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-A" {
		t.Errorf("got %q, want %q", got, "start-A")
	}
}
