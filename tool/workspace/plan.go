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

// Package workspace implements the LLM-facing tools that read and write a
// per-session Plan Workspace (vage/workspace). It is intentionally narrow:
// plan_update / notes_write / notes_read with strict argument validation,
// no path arguments, and structured event emission so SessionHook /
// tracelog observers receive a typed snapshot per write.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/workspace"
)

// PlanToolName is the registered name for the plan_update tool.
const PlanToolName = "plan_update"

const planToolDescription = `Replace the entire plan.md for the current session.

WHEN to use:
  - The user gave a multi-step task that may span turns or sessions; record progress before continuing.
  - You finished a step and want to mark it done so a future-you can resume.
  - You re-scoped a plan (added/removed steps).

HOW to use:
  - Pass the FULL new plan as ` + "`content`" + `. The previous plan is overwritten.
  - Use Markdown checkboxes (- [x] / - [ ]) for granular steps.
  - Keep it terse — strategy doc, not a journal.
  - Pass content="" to clear the plan.

DO NOT:
  - Use this for short-lived TODO items inside a single ReAct loop — that's todo_write.
    plan.md persists across sessions; todo_write does not.
  - Treat plan.md as a notepad for facts. Use notes_write for facts.
  - Worry about a file path — the tool always targets plan.md for the current session.`

var planParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"content": map[string]any{
			"type":        "string",
			"description": "Full plan markdown. Pass empty string to clear.",
		},
	},
	"required": []string{"content"},
}

// PlanTool wires the plan_update handler to a workspace.Workspace.
type PlanTool struct {
	ws workspace.Workspace
}

// NewPlanTool constructs a PlanTool. ws must not be nil.
func NewPlanTool(ws workspace.Workspace) *PlanTool {
	return &PlanTool{ws: ws}
}

// ToolDef returns the schema.ToolDef for registration. ReadOnly is false
// because plan_update touches the on-disk workspace; permission gates that
// distinguish read-only vs write tools should treat it as a write.
func (t *PlanTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        PlanToolName,
		Description: planToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  planParametersSchema,
	}
}

// Handler returns the ToolHandler closure. It reads the sessionID from ctx
// (TaskAgent injects it via schema.WithSessionID), writes plan.md via the
// workspace, and emits EventWorkspacePlanUpdated through the optional
// Emitter so trace + session hooks see a structured snapshot.
func (t *PlanTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "plan_update: session id missing from context"), nil
		}

		var parsed struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "plan_update: invalid arguments: "+err.Error()), nil
		}

		if err := t.ws.WritePlan(ctx, sessionID, parsed.Content); err != nil {
			return schema.ErrorResult("", "plan_update: "+err.Error()), nil
		}

		bytesWritten := len(parsed.Content)
		cleared := bytesWritten == 0

		if em := schema.EmitterFromContext(ctx); em != nil {
			_ = em(schema.NewEvent(
				schema.EventWorkspacePlanUpdated, "", sessionID,
				schema.WorkspacePlanUpdatedData{
					SessionID: sessionID,
					Bytes:     bytesWritten,
					Cleared:   cleared,
				},
			))
		}

		var msg string
		if cleared {
			msg = "ok (plan cleared)"
		} else {
			msg = fmt.Sprintf("ok (%d bytes written to plan.md)", bytesWritten)
		}
		return schema.TextResult("", msg), nil
	}
}

// RegisterPlan creates a PlanTool bound to ws and registers it under reg.
// Returns an error if ws is nil or the tool name is already registered.
func RegisterPlan(reg *tool.Registry, ws workspace.Workspace) error {
	if ws == nil {
		return errors.New("workspace.RegisterPlan: workspace is nil")
	}
	t := NewPlanTool(ws)
	return reg.RegisterIfAbsent(t.ToolDef(), t.Handler())
}
