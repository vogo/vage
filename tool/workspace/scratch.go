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
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/workspace"
)

// ScratchWriteToolName is the registered name for the scratch_write tool.
const ScratchWriteToolName = "scratch_write"

// ScratchReadToolName is the registered name for the scratch_read tool.
const ScratchReadToolName = "scratch_read"

// ScratchListToolName is the registered name for the scratch_list tool.
const ScratchListToolName = "scratch_list"

const scratchWriteToolDescription = `Write a transient draft entry into your private scratch slot.

WHEN to use:
  - You are working on a multi-step subtask and need to keep an intermediate
    note across iterations (a partial plan, a chunk of analysis, an in-flight
    diff) that should NOT pollute the parent's notes/.
  - You want to retry a failed step without losing prior context — scratch
    is per-subtask and gets wiped when the dispatcher cleans up.

HOW to use:
  - One file per draft. Names must match [A-Za-z0-9._-]{1,64}; the .md suffix
    is added for you, do NOT include it in 'name'.
  - Pass empty 'content' to delete the entry.

DO NOT:
  - Treat scratch as long-term memory — use notes_write for facts you want
    to recall across runs. Scratch is short-lived by contract.`

const scratchReadToolDescription = `Read the full body of a single scratch entry from your slot.

WHEN to use:
  - You wrote a draft earlier in this subtask and want to consult it again.

HOW:
  - Pass the same 'name' you used in scratch_write (no extension).`

const scratchListToolDescription = `List entries currently in your scratch slot.

WHEN to use:
  - You want to see what drafts are already saved before deciding what to write.
  - You want to enumerate work-in-progress before consolidating into a final note.

Returns name, byte size, and last-updated time, ordered most-recent first.`

var scratchWriteParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Scratch entry name; alphanumerics + . _ - only, ≤ 64 chars (no .md extension).",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Markdown content. Pass empty string to delete the entry.",
		},
	},
	"required": []string{"name", "content"},
}

var scratchReadParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Scratch entry name (no .md extension).",
		},
	},
	"required": []string{"name"},
}

var scratchListParametersSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{},
}

// ScratchWriteTool writes per-subtask draft entries. The slot is bound
// at registration time so the LLM never names other slots — this is
// the security/scoping promise of the agent-as-tool dispatch path.
type ScratchWriteTool struct {
	ws   workspace.Workspace
	slot string
}

// NewScratchWriteTool constructs a ScratchWriteTool bound to slot.
func NewScratchWriteTool(ws workspace.Workspace, slot string) *ScratchWriteTool {
	return &ScratchWriteTool{ws: ws, slot: slot}
}

// ToolDef returns the schema.ToolDef for the scratch_write tool.
func (t *ScratchWriteTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ScratchWriteToolName,
		Description: scratchWriteToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  scratchWriteParametersSchema,
	}
}

// Handler returns the ToolHandler closure for scratch_write.
func (t *ScratchWriteTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "scratch_write: session id missing from context"), nil
		}

		var parsed struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "scratch_write: invalid arguments: "+err.Error()), nil
		}

		if err := t.ws.WriteScratch(ctx, sessionID, t.slot, parsed.Name, parsed.Content); err != nil {
			return schema.ErrorResult("", "scratch_write: "+err.Error()), nil
		}

		bytesWritten := len(parsed.Content)
		cleared := bytesWritten == 0

		if em := schema.EmitterFromContext(ctx); em != nil {
			_ = em(schema.NewEvent(
				schema.EventWorkspaceScratchWritten, "", sessionID,
				schema.WorkspaceScratchWrittenData{
					SessionID: sessionID,
					Slot:      t.slot,
					Name:      parsed.Name,
					Bytes:     bytesWritten,
					Cleared:   cleared,
				},
			))
		}

		var msg string
		if cleared {
			msg = fmt.Sprintf("ok (scratch %q deleted)", parsed.Name)
		} else {
			msg = fmt.Sprintf("ok (%d bytes written to scratch/%s/%s.md)", bytesWritten, t.slot, parsed.Name)
		}
		return schema.TextResult("", msg), nil
	}
}

// ScratchReadTool reads from the bound slot.
type ScratchReadTool struct {
	ws   workspace.Workspace
	slot string
}

// NewScratchReadTool constructs a ScratchReadTool bound to slot.
func NewScratchReadTool(ws workspace.Workspace, slot string) *ScratchReadTool {
	return &ScratchReadTool{ws: ws, slot: slot}
}

// ToolDef returns the schema.ToolDef for the scratch_read tool.
func (t *ScratchReadTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ScratchReadToolName,
		Description: scratchReadToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  scratchReadParametersSchema,
	}
}

// Handler returns the ToolHandler closure for scratch_read.
func (t *ScratchReadTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "scratch_read: session id missing from context"), nil
		}

		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "scratch_read: invalid arguments: "+err.Error()), nil
		}

		content, err := t.ws.ReadScratch(ctx, sessionID, t.slot, parsed.Name)
		if err != nil {
			return schema.ErrorResult("", "scratch_read: "+err.Error()), nil
		}
		if content == "" {
			return schema.TextResult("", fmt.Sprintf("(no scratch entry %q)", parsed.Name)), nil
		}
		return schema.TextResult("", content), nil
	}
}

// ScratchListTool lists entries in the bound slot.
type ScratchListTool struct {
	ws   workspace.Workspace
	slot string
}

// NewScratchListTool constructs a ScratchListTool bound to slot.
func NewScratchListTool(ws workspace.Workspace, slot string) *ScratchListTool {
	return &ScratchListTool{ws: ws, slot: slot}
}

// ToolDef returns the schema.ToolDef for the scratch_list tool.
func (t *ScratchListTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ScratchListToolName,
		Description: scratchListToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  scratchListParametersSchema,
	}
}

// Handler returns the ToolHandler closure for scratch_list.
func (t *ScratchListTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "scratch_list: session id missing from context"), nil
		}
		entries, err := t.ws.ListScratch(ctx, sessionID, t.slot)
		if err != nil {
			return schema.ErrorResult("", "scratch_list: "+err.Error()), nil
		}
		if len(entries) == 0 {
			return schema.TextResult("", fmt.Sprintf("(no entries in scratch slot %q)", t.slot)), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "scratch slot %q (%d entries):\n", t.slot, len(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s (%d bytes, %s)\n", e.Name, e.Bytes, e.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		return schema.TextResult("", b.String()), nil
	}
}

// RegisterScratch creates the three scratch_* tools bound to ws and slot
// and registers them in reg. Returns an error on duplicate name, nil ws,
// or empty slot id.
func RegisterScratch(reg *tool.Registry, ws workspace.Workspace, slot string) error {
	if ws == nil {
		return errors.New("workspace.RegisterScratch: workspace is nil")
	}
	if slot == "" {
		return errors.New("workspace.RegisterScratch: slot is empty")
	}
	wt := NewScratchWriteTool(ws, slot)
	if err := reg.RegisterIfAbsent(wt.ToolDef(), wt.Handler()); err != nil {
		return err
	}
	rt := NewScratchReadTool(ws, slot)
	if err := reg.RegisterIfAbsent(rt.ToolDef(), rt.Handler()); err != nil {
		return err
	}
	lt := NewScratchListTool(ws, slot)
	return reg.RegisterIfAbsent(lt.ToolDef(), lt.Handler())
}
