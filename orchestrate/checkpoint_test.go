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
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestInMemoryCheckpointStore_SaveLoadClear(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	ctx := context.Background()

	resp := &schema.RunResponse{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "result")},
	}

	// Save.
	if err := store.Save(ctx, "dag1", "nodeA", resp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load.
	loaded, err := store.Load(ctx, "dag1", "nodeA")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded response")
	}
	if loaded.Messages[0].Text() != "result" {
		t.Errorf("got %q, want %q", loaded.Messages[0].Text(), "result")
	}

	// Load non-existent.
	loaded, err = store.Load(ctx, "dag1", "nodeB")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil for non-existent node")
	}

	// LoadAll.
	all, err := store.LoadAll(ctx, "dag1")
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 result, got %d", len(all))
	}

	// Clear.
	if err := store.Clear(ctx, "dag1"); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	all, err = store.LoadAll(ctx, "dag1")
	if err != nil {
		t.Fatalf("LoadAll after Clear failed: %v", err)
	}
	if all != nil {
		t.Error("expected nil after Clear")
	}
}

func TestInMemoryCheckpointStore_MultipleDags(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	ctx := context.Background()

	resp1 := &schema.RunResponse{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "dag1-result")}}
	resp2 := &schema.RunResponse{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "dag2-result")}}

	_ = store.Save(ctx, "dag1", "node1", resp1)
	_ = store.Save(ctx, "dag2", "node1", resp2)

	all1, _ := store.LoadAll(ctx, "dag1")
	all2, _ := store.LoadAll(ctx, "dag2")

	if len(all1) != 1 || len(all2) != 1 {
		t.Error("expected separate DAG storage")
	}
	if all1["node1"].Messages[0].Text() != "dag1-result" {
		t.Error("dag1 data mismatch")
	}
	if all2["node1"].Messages[0].Text() != "dag2-result" {
		t.Error("dag2 data mismatch")
	}
}

func TestDAG_CheckpointResume(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	ctx := context.Background()

	// Pre-populate checkpoint: A is already done.
	aResp := &schema.RunResponse{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start-A")}}
	_ = store.Save(ctx, "test-session", "A", aResp)

	var aExecuted atomic.Bool
	nodes := []Node{
		{ID: "A", Runner: newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			aExecuted.Store(true)
			return &schema.RunResponse{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start-A")}}, nil
		})},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}

	cfg := DAGConfig{
		CheckpointStore: store,
	}
	result, err := ExecuteDAG(ctx, cfg, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A should not have been re-executed because it was checkpointed.
	if aExecuted.Load() {
		t.Error("A should not have been re-executed")
	}

	// B should have completed.
	if result.NodeStatus["B"] != NodeDone {
		t.Errorf("B status = %d, want NodeDone", result.NodeStatus["B"])
	}
	got := result.FinalOutput.Messages[0].Text()
	if got != "start-A-B" {
		t.Errorf("got %q, want %q", got, "start-A-B")
	}
}

func TestDAG_CheckpointSave(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	ctx := context.Background()

	nodes := []Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}

	cfg := DAGConfig{
		CheckpointStore: store,
	}
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "save-test",
	}
	_, err := ExecuteDAG(ctx, cfg, nodes, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify checkpoints were saved.
	all, _ := store.LoadAll(ctx, "save-test")
	if len(all) != 2 {
		t.Errorf("expected 2 checkpoints, got %d", len(all))
	}
	if all["A"] == nil {
		t.Error("checkpoint for A missing")
	}
	if all["B"] == nil {
		t.Error("checkpoint for B missing")
	}
}
