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
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/largemodel/schema"
)

func TestNode_Timeout(t *testing.T) {
	slowRunner := newMockRunner(func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &schema.RunResponse{Messages: req.Messages}, nil
		}
	})

	nodes := []Node{
		{ID: "A", Runner: slowRunner, Timeout: 50 * time.Millisecond},
	}

	_, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "node \"A\" failed") {
		t.Errorf("error should mention node A: %v", err)
	}
}

func TestNode_Retries(t *testing.T) {
	var callCount atomic.Int32

	flakyRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		count := callCount.Add(1)
		if count <= 2 {
			return nil, errors.New("transient error")
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []Node{
		{ID: "A", Runner: flakyRunner, Retries: 3},
	}

	result, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["A"] != NodeDone {
		t.Errorf("A status = %d, want NodeDone", result.NodeStatus["A"])
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", callCount.Load())
	}
}

func TestNode_Retries_AllFail(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: errorRunner(errors.New("permanent error")), Retries: 2},
	}

	_, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err == nil {
		t.Fatal("expected error after all retries exhausted")
	}
	if !strings.Contains(err.Error(), "permanent error") {
		t.Errorf("error should contain original: %v", err)
	}
}

func TestNode_TimeoutWithRetries(t *testing.T) {
	var callCount atomic.Int32

	runner := newMockRunner(func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		count := callCount.Add(1)
		if count <= 1 {
			// First call times out.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return &schema.RunResponse{Messages: req.Messages}, nil
			}
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []Node{
		{ID: "A", Runner: runner, Timeout: 50 * time.Millisecond, Retries: 2},
	}

	result, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["A"] != NodeDone {
		t.Errorf("A status = %d, want NodeDone", result.NodeStatus["A"])
	}
}

func TestNode_ResourceTags(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: passthroughRunner(), ResourceTags: []string{"gpu", "api"}},
	}

	if len(nodes[0].ResourceTags) != 2 {
		t.Errorf("expected 2 resource tags, got %d", len(nodes[0].ResourceTags))
	}
}

func TestNode_Priority(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: passthroughRunner(), Priority: 10},
		{ID: "B", Runner: passthroughRunner(), Priority: 0},
	}

	if nodes[0].Priority != 10 {
		t.Errorf("A priority = %d, want 10", nodes[0].Priority)
	}
	if nodes[1].Priority != 0 {
		t.Errorf("B priority = %d, want 0", nodes[1].Priority)
	}
}
