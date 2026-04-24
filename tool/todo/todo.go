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

package todo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// ToolName is the registered name for the todo_write tool.
const ToolName = "todo_write"

const toolDescription = `Use this tool to plan and track multi-step work. The list you submit REPLACES the previous list entirely — pass all items every call.

WHEN to use (proactively, without being asked):
  - the task has 3 or more distinct steps
  - the task is non-trivial and would benefit from a written plan
  - the user provides multiple tasks or asks for planning

WHEN NOT to use:
  - a single trivial change (read one file, tweak one line)
  - purely informational answers
  - tasks that finish in one tool call

STATUS LIFECYCLE (must follow):
  pending -> in_progress -> completed
  - Mark an item in_progress BEFORE you start working on it.
  - Mark it completed IMMEDIATELY when it's done. Do not batch.
  - Only ONE item may be in_progress at a time. The tool will reject
    calls that violate this invariant.
  - If you abandon an item, remove it from the list.

FIELDS:
  content:     imperative present tense, e.g. "Fix the auth bug"
  active_form: present continuous, e.g. "Fixing the auth bug"
  status:      one of {pending, in_progress, completed}
  id:          optional; if you keep the list stable across calls,
               the server-assigned id lets the UI diff cleanly

DO NOT:
  - create TODO.md files or other side-channel tracking files; use only this tool
  - announce "I'll update the todo list" without actually calling this tool
  - flip multiple items to completed in one call without having executed them`

// parametersSchema is the JSON Schema used for the tool's arguments.
var parametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"todos": map[string]any{
			"type":        "array",
			"description": "Full list of todos (replaces the current list entirely). Pass [] or null to clear.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "optional stable id; server will assign if empty",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "imperative form, e.g., 'Refactor foo module'",
					},
					"active_form": map[string]any{
						"type":        "string",
						"description": "present-continuous form, e.g., 'Refactoring foo module'",
					},
					"status": map[string]any{
						"type": "string",
						"enum": []string{
							string(StatusPending),
							string(StatusInProgress),
							string(StatusCompleted),
						},
					},
				},
				"required": []string{"content", "active_form", "status"},
			},
		},
	},
	"required": []string{"todos"},
}

// Tool wires the todo_write handler to a shared Store.
type Tool struct {
	store *Store
}

// New creates a Tool bound to the given Store.
func New(store *Store) *Tool {
	return &Tool{store: store}
}

// ToolDef returns the schema.ToolDef for registration. ReadOnly is true
// because todo_write does not touch the workspace filesystem or any external
// side effect — it writes session-scoped memory only.
func (t *Tool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ToolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  parametersSchema,
	}
}

// Handler returns the ToolHandler closure. It reads the sessionID and optional
// Emitter from ctx (injected by TaskAgent's executeToolBatch), applies the new
// list to the Store, emits an EventTodoUpdate with the full snapshot, and
// returns a minimal confirmation text to the LLM.
func (t *Tool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "todo_write: session id missing from context"), nil
		}

		var parsed struct {
			Todos []Item `json:"todos"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "todo_write: invalid arguments: "+err.Error()), nil
		}

		snap, err := t.store.Apply(sessionID, parsed.Todos)
		if err != nil {
			return schema.ErrorResult("", "todo_write: "+err.Error()), nil
		}

		if em := schema.EmitterFromContext(ctx); em != nil {
			_ = em(schema.NewEvent(
				schema.EventTodoUpdate, "", sessionID,
				toEventData(snap),
			))
		}

		return schema.TextResult("", fmt.Sprintf("ok (v%d, %d items)", snap.Version, len(snap.Items))), nil
	}
}

// Register creates a Tool bound to store and registers it in reg. Returns an
// error if the tool name is already registered or store is nil.
func Register(reg *tool.Registry, store *Store) error {
	if store == nil {
		return errors.New("todo.Register: store is nil")
	}
	t := New(store)
	return reg.RegisterIfAbsent(t.ToolDef(), t.Handler())
}

// toEventData projects an internal Snapshot into the wire schema type.
func toEventData(snap Snapshot) schema.TodoUpdateData {
	out := schema.TodoUpdateData{Version: snap.Version}
	if len(snap.Items) == 0 {
		return out
	}
	out.Items = make([]schema.TodoItem, len(snap.Items))
	for i, it := range snap.Items {
		out.Items[i] = schema.TodoItem{
			ID:         it.ID,
			Content:    it.Content,
			ActiveForm: it.ActiveForm,
			Status:     string(it.Status),
		}
	}
	return out
}
