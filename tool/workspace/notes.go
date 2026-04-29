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

// NotesWriteToolName is the registered name for the notes_write tool.
const NotesWriteToolName = "notes_write"

// NotesReadToolName is the registered name for the notes_read tool.
const NotesReadToolName = "notes_read"

const notesWriteToolDescription = `Write a long-lived note for the current session.

WHEN to use:
  - You discovered a fact you want to recall later (decisions, file paths,
    config snippets, learned conventions, blocked items).
  - You finished a sub-investigation and want to capture the conclusion.

HOW to use:
  - One file per topic. Names must match [A-Za-z0-9._-]{1,64}; the .md
    suffix is added for you, do NOT include it in 'name'.
  - Pass empty 'content' to delete a note.

DO NOT:
  - Use notes_write as a journal of every step (that's events.jsonl;
    you do not write to it).
  - Treat notes as your plan — plan.md is the plan; notes are facts.`

const notesReadToolDescription = `Read the full body of a single note.

WHEN to use:
  - The notes index injected at the top of your prompt shows a relevant note
    name; read its content to recall the details.
  - Pull a saved snippet you wrote earlier in this or a prior session.

HOW:
  - Pass the same 'name' you used in notes_write (no extension).`

var notesWriteParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Note name; alphanumerics + . _ - only, ≤ 64 chars (no .md extension).",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Note markdown content. Pass empty string to delete the note.",
		},
	},
	"required": []string{"name", "content"},
}

var notesReadParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Note name (no .md extension).",
		},
	},
	"required": []string{"name"},
}

// NotesWriteTool wires the notes_write handler to a workspace.Workspace.
type NotesWriteTool struct {
	ws workspace.Workspace
}

// NewNotesWriteTool constructs a NotesWriteTool.
func NewNotesWriteTool(ws workspace.Workspace) *NotesWriteTool {
	return &NotesWriteTool{ws: ws}
}

// ToolDef returns the schema.ToolDef for the notes_write tool.
func (t *NotesWriteTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        NotesWriteToolName,
		Description: notesWriteToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  notesWriteParametersSchema,
	}
}

// Handler returns the ToolHandler closure for notes_write.
func (t *NotesWriteTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "notes_write: session id missing from context"), nil
		}

		var parsed struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "notes_write: invalid arguments: "+err.Error()), nil
		}

		if err := t.ws.WriteNote(ctx, sessionID, parsed.Name, parsed.Content); err != nil {
			return schema.ErrorResult("", "notes_write: "+err.Error()), nil
		}

		bytesWritten := len(parsed.Content)
		cleared := bytesWritten == 0

		if em := schema.EmitterFromContext(ctx); em != nil {
			_ = em(schema.NewEvent(
				schema.EventWorkspaceNoteWritten, "", sessionID,
				schema.WorkspaceNoteWrittenData{
					SessionID: sessionID,
					Name:      parsed.Name,
					Bytes:     bytesWritten,
					Cleared:   cleared,
				},
			))
		}

		var msg string
		if cleared {
			msg = fmt.Sprintf("ok (note %q deleted)", parsed.Name)
		} else {
			msg = fmt.Sprintf("ok (%d bytes written to notes/%s.md)", bytesWritten, parsed.Name)
		}
		return schema.TextResult("", msg), nil
	}
}

// NotesReadTool wires the notes_read handler to a workspace.Workspace.
type NotesReadTool struct {
	ws workspace.Workspace
}

// NewNotesReadTool constructs a NotesReadTool.
func NewNotesReadTool(ws workspace.Workspace) *NotesReadTool {
	return &NotesReadTool{ws: ws}
}

// ToolDef returns the schema.ToolDef for the notes_read tool.
func (t *NotesReadTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        NotesReadToolName,
		Description: notesReadToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  notesReadParametersSchema,
	}
}

// Handler returns the ToolHandler closure for notes_read.
func (t *NotesReadTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "notes_read: session id missing from context"), nil
		}

		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "notes_read: invalid arguments: "+err.Error()), nil
		}

		content, err := t.ws.ReadNote(ctx, sessionID, parsed.Name)
		if err != nil {
			return schema.ErrorResult("", "notes_read: "+err.Error()), nil
		}

		if content == "" {
			return schema.TextResult("", fmt.Sprintf("(no note %q)", parsed.Name)), nil
		}
		return schema.TextResult("", content), nil
	}
}

// RegisterNotes creates the notes_write and notes_read tools bound to ws and
// registers them in reg. Returns an error on duplicate name or nil ws.
func RegisterNotes(reg *tool.Registry, ws workspace.Workspace) error {
	if ws == nil {
		return errors.New("workspace.RegisterNotes: workspace is nil")
	}
	wt := NewNotesWriteTool(ws)
	if err := reg.RegisterIfAbsent(wt.ToolDef(), wt.Handler()); err != nil {
		return err
	}
	rt := NewNotesReadTool(ws)
	return reg.RegisterIfAbsent(rt.ToolDef(), rt.Handler())
}
