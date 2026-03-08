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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/vogo/vagent/schema"
)

// TaskStatus represents the lifecycle state of an async task.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// ErrTooManyTasks is returned when the task store has reached its capacity.
var ErrTooManyTasks = errors.New("too many pending tasks")

// Task represents an asynchronous agent execution.
type Task struct {
	ID        string              `json:"id"`
	AgentID   string              `json:"agent_id"`
	Status    TaskStatus          `json:"status"`
	Response  *schema.RunResponse `json:"response,omitempty"`
	Error     string              `json:"error,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`

	cancel context.CancelFunc `json:"-"`
}

// TaskStore is a thread-safe in-memory store for async tasks.
type TaskStore struct {
	mu       sync.RWMutex
	tasks    map[string]*Task
	maxTasks int
}

// NewTaskStore creates an empty TaskStore with the given capacity limit.
func NewTaskStore(maxTasks int) *TaskStore {
	return &TaskStore{
		tasks:    make(map[string]*Task),
		maxTasks: maxTasks,
	}
}

// Create creates a new pending task and returns it.
// It returns ErrTooManyTasks if the store has reached its capacity.
func (s *TaskStore) Create(agentID string, cancel context.CancelFunc) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxTasks > 0 && len(s.tasks) >= s.maxTasks {
		// Try to evict one completed/failed task to make room.
		if !s.evictOneLocked() {
			return nil, ErrTooManyTasks
		}
	}

	id := generateTaskID()
	now := time.Now()

	t := &Task{
		ID:        id,
		AgentID:   agentID,
		Status:    TaskStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
		cancel:    cancel,
	}

	s.tasks[id] = t

	return t, nil
}

// evictOneLocked removes the oldest completed or failed task. Must be called with mu held.
func (s *TaskStore) evictOneLocked() bool {
	var oldestID string
	var oldestTime time.Time

	for id, t := range s.tasks {
		if t.Status != TaskStatusCompleted && t.Status != TaskStatusFailed && t.Status != TaskStatusCancelled {
			continue
		}
		if oldestID == "" || t.UpdatedAt.Before(oldestTime) {
			oldestID = id
			oldestTime = t.UpdatedAt
		}
	}

	if oldestID == "" {
		return false
	}

	delete(s.tasks, oldestID)

	return true
}

// Get returns a task by ID.
func (s *TaskStore) Get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}

	// Return a copy to avoid data races.
	cp := *t
	cp.cancel = nil

	return &cp, true
}

// UpdateStatus updates the status of a task.
func (s *TaskStore) UpdateStatus(id string, status TaskStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t, ok := s.tasks[id]; ok {
		t.Status = status
		t.UpdatedAt = time.Now()
	}
}

// SetResult sets the response or error on a completed/failed task.
func (s *TaskStore) SetResult(id string, resp *schema.RunResponse, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return
	}

	// Do not overwrite a cancelled task.
	if t.Status == TaskStatusCancelled {
		return
	}

	t.UpdatedAt = time.Now()

	if err != nil {
		t.Status = TaskStatusFailed
		t.Error = err.Error()
	} else {
		t.Status = TaskStatusCompleted
		t.Response = resp
	}
}

// Cancel cancels a running or pending task.
func (s *TaskStore) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return false
	}

	if t.Status != TaskStatusPending && t.Status != TaskStatusRunning {
		return false
	}

	t.Status = TaskStatusCancelled
	t.UpdatedAt = time.Now()

	if t.cancel != nil {
		t.cancel()
	}

	return true
}

func generateTaskID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
