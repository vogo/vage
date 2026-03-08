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
	"errors"
	"testing"

	"github.com/vogo/vagent/schema"
)

func TestTaskStore_CreateAndGet(t *testing.T) {
	store := NewTaskStore(100)
	task, err := store.Create("agent-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.ID == "" {
		t.Fatal("expected non-empty task ID")
	}

	if task.AgentID != "agent-1" {
		t.Fatalf("expected agent ID %q, got %q", "agent-1", task.AgentID)
	}

	if task.Status != TaskStatusPending {
		t.Fatalf("expected status %q, got %q", TaskStatusPending, task.Status)
	}

	got, ok := store.Get(task.ID)
	if !ok {
		t.Fatal("expected to find task")
	}

	if got.ID != task.ID {
		t.Fatalf("expected ID %q, got %q", task.ID, got.ID)
	}
}

func TestTaskStore_GetNotFound(t *testing.T) {
	store := NewTaskStore(100)

	_, ok := store.Get("nonexistent")
	if ok {
		t.Fatal("expected task not found")
	}
}

func TestTaskStore_UpdateStatus(t *testing.T) {
	store := NewTaskStore(100)
	task, _ := store.Create("agent-1", nil)

	store.UpdateStatus(task.ID, TaskStatusRunning)

	got, _ := store.Get(task.ID)
	if got.Status != TaskStatusRunning {
		t.Fatalf("expected status %q, got %q", TaskStatusRunning, got.Status)
	}
}

func TestTaskStore_SetResultSuccess(t *testing.T) {
	store := NewTaskStore(100)
	task, _ := store.Create("agent-1", nil)

	resp := &schema.RunResponse{SessionID: "s1"}
	store.SetResult(task.ID, resp, nil)

	got, _ := store.Get(task.ID)
	if got.Status != TaskStatusCompleted {
		t.Fatalf("expected status %q, got %q", TaskStatusCompleted, got.Status)
	}

	if got.Response == nil || got.Response.SessionID != "s1" {
		t.Fatal("expected response with session ID s1")
	}
}

func TestTaskStore_SetResultError(t *testing.T) {
	store := NewTaskStore(100)
	task, _ := store.Create("agent-1", nil)

	store.SetResult(task.ID, nil, errors.New("something went wrong"))

	got, _ := store.Get(task.ID)
	if got.Status != TaskStatusFailed {
		t.Fatalf("expected status %q, got %q", TaskStatusFailed, got.Status)
	}

	if got.Error != "something went wrong" {
		t.Fatalf("expected error %q, got %q", "something went wrong", got.Error)
	}
}

func TestTaskStore_Cancel(t *testing.T) {
	cancelled := false
	store := NewTaskStore(100)
	task, _ := store.Create("agent-1", func() { cancelled = true })

	store.UpdateStatus(task.ID, TaskStatusRunning)

	if !store.Cancel(task.ID) {
		t.Fatal("expected cancel to succeed")
	}

	if !cancelled {
		t.Fatal("expected cancel function to be called")
	}

	got, _ := store.Get(task.ID)
	if got.Status != TaskStatusCancelled {
		t.Fatalf("expected status %q, got %q", TaskStatusCancelled, got.Status)
	}
}

func TestTaskStore_CancelCompletedTask(t *testing.T) {
	store := NewTaskStore(100)
	task, _ := store.Create("agent-1", nil)

	store.SetResult(task.ID, &schema.RunResponse{}, nil)

	if store.Cancel(task.ID) {
		t.Fatal("expected cancel of completed task to fail")
	}
}

func TestTaskStore_MaxTasksEviction(t *testing.T) {
	store := NewTaskStore(2)

	t1, _ := store.Create("agent-1", nil)
	_, _ = store.Create("agent-2", nil)

	// Mark first task as completed so it can be evicted.
	store.SetResult(t1.ID, &schema.RunResponse{}, nil)

	// Third create should succeed by evicting the completed task.
	_, err := store.Create("agent-3", nil)
	if err != nil {
		t.Fatalf("expected create to succeed after eviction, got: %v", err)
	}
}

func TestTaskStore_MaxTasksNoEviction(t *testing.T) {
	store := NewTaskStore(2)

	_, _ = store.Create("agent-1", nil)
	_, _ = store.Create("agent-2", nil)

	// Both tasks are pending, so eviction should fail.
	_, err := store.Create("agent-3", nil)
	if !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("expected ErrTooManyTasks, got: %v", err)
	}
}
