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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// handleHealth responds with a simple health check.
func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// agentSummary is the JSON representation for agent listing.
type agentSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleListAgents returns all registered agents sorted by ID.
func (s *Service) handleListAgents(w http.ResponseWriter, _ *http.Request) {
	agents := s.listAgentsSorted()

	summaries := make([]agentSummary, 0, len(agents))
	for _, a := range agents {
		summaries = append(summaries, agentSummary{
			ID:          a.ID(),
			Name:        a.Name(),
			Description: a.Description(),
		})
	}

	writeJSON(w, http.StatusOK, summaries)
}

// handleGetAgent returns details for a single agent.
func (s *Service) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	a, ok := s.getAgent(id)
	if !ok {
		writeError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found", id))
		return
	}

	writeJSON(w, http.StatusOK, agentSummary{
		ID:          a.ID(),
		Name:        a.Name(),
		Description: a.Description(),
	})
}

// handleRun executes an agent synchronously.
func (s *Service) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	a, ok := s.getAgent(id)
	if !ok {
		writeError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found", id))
		return
	}

	var req schema.RunRequest
	if err := decodeBody(r, s.maxRequestSize, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body: "+err.Error())
		return
	}

	resp, err := a.Run(r.Context(), &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agent_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleStream executes an agent and returns SSE events.
func (s *Service) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	a, ok := s.getAgent(id)
	if !ok {
		writeError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found", id))
		return
	}

	var req schema.RunRequest
	if err := decodeBody(r, s.maxRequestSize, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body: "+err.Error())
		return
	}

	// Get the stream: use StreamAgent if available, otherwise wrap via RunToStream.
	var rs *schema.RunStream

	if sa, ok := a.(agent.StreamAgent); ok {
		var err error
		rs, err = sa.RunStream(r.Context(), &req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_error", err.Error())
			return
		}
	} else {
		rs = agent.RunToStream(r.Context(), a, &req)
	}

	// Write SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	heartbeat := time.NewTicker(time.Duration(s.heartbeatSec) * time.Second)
	defer heartbeat.Stop()

	events := make(chan sseItem, 64)

	// Receive events from stream in a goroutine.
	go func() {
		defer close(events)
		for {
			e, err := rs.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				data, _ := json.Marshal(ErrorResponse{Code: "stream_error", Message: err.Error()})
				events <- sseItem{event: "error", data: data}
				return
			}
			data, _ := json.Marshal(e)
			events <- sseItem{event: e.Type, data: data}
		}
	}()

	for {
		select {
		case item, ok := <-events:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", item.event, item.data)
			if flusher != nil {
				flusher.Flush()
			}
			if item.event == "error" {
				return
			}
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

type sseItem struct {
	event string
	data  []byte
}

// handleAsync starts an agent execution asynchronously.
func (s *Service) handleAsync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	a, ok := s.getAgent(id)
	if !ok {
		writeError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found", id))
		return
	}

	var req schema.RunRequest
	if err := decodeBody(r, s.maxRequestSize, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body: "+err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	task, err := s.tasks.Create(id, cancel)
	if err != nil {
		cancel()
		writeError(w, http.StatusTooManyRequests, "too_many_tasks", err.Error())
		return
	}

	// Run agent in background with a cancellable context.
	go func() {
		defer cancel()
		s.tasks.UpdateStatus(task.ID, TaskStatusRunning)
		resp, err := a.Run(ctx, &req)
		s.tasks.SetResult(task.ID, resp, err)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"task_id": task.ID})
}

// handleGetTask returns the status and result of an async task.
func (s *Service) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")

	task, ok := s.tasks.Get(taskID)
	if !ok {
		writeError(w, http.StatusNotFound, "task_not_found", fmt.Sprintf("task %q not found", taskID))
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleCancelTask cancels a pending or running async task.
func (s *Service) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")

	if !s.tasks.Cancel(taskID) {
		writeError(w, http.StatusNotFound, "task_not_found", fmt.Sprintf("task %q not found or not cancellable", taskID))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleListTools returns all registered tools.
func (s *Service) handleListTools(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	reg := s.tools
	s.mu.RUnlock()

	if reg == nil {
		writeJSON(w, http.StatusOK, []schema.ToolDef{})
		return
	}

	writeJSON(w, http.StatusOK, reg.List())
}

// decodeBody reads and decodes a JSON request body with size limit.
func decodeBody(r *http.Request, maxSize int64, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxSize)
	return json.NewDecoder(r.Body).Decode(v)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standardized error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Code: code, Message: message})
}
