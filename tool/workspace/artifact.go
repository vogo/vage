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

// ArtifactWriteToolName is the registered name for the artifact_write tool.
const ArtifactWriteToolName = "artifact_write"

// ArtifactReadToolName is the registered name for the artifact_read tool.
const ArtifactReadToolName = "artifact_read"

const artifactWriteToolDescription = `Persist a large or long-lived produced output (diff, report, log, big tool dump) to the session's artifacts/ store.

WHEN to use:
  - You generated a diff, report, or sizable log that the user (or a
    follow-up step) may want to retrieve verbatim later.
  - You hit a tool result so big it would clutter the prompt — the
    framework already auto-elides oversized tool_result bodies, but you
    can also write artifacts proactively when you know the output is the
    deliverable.

HOW to use:
  - One file per artifact. Names must match [A-Za-z0-9._-]{1,64}; the
    extension you provide is preserved exactly (e.g. "diff.patch", "log.txt").
  - Artifacts are write-once references. Empty content writes an empty
    file (NOT a delete) — there is no per-artifact delete; rely on
    session deletion to clean up.

DO NOT:
  - Use artifacts as your scratch area — use scratch_write for that.
  - Use artifacts as your long-term notes — use notes_write.`

const artifactReadToolDescription = `Read the full body of a previously-written artifact.

WHEN to use:
  - You wrote the artifact in this or an earlier turn and need to consult it.
  - The framework's auto-elision placeholder pointed you at an
    artifacts/<name> reference and you want the body back in context.

HOW:
  - Pass the same 'name' you used in artifact_write (extension included).`

var artifactWriteParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Artifact name including extension; alphanumerics + . _ - only, ≤ 64 chars.",
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Artifact content (UTF-8 text).",
		},
	},
	"required": []string{"name", "content"},
}

var artifactReadParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Artifact name (with extension).",
		},
	},
	"required": []string{"name"},
}

// ArtifactWriteTool wires the artifact_write handler to a Workspace.
type ArtifactWriteTool struct {
	ws workspace.Workspace
}

// NewArtifactWriteTool constructs an ArtifactWriteTool.
func NewArtifactWriteTool(ws workspace.Workspace) *ArtifactWriteTool {
	return &ArtifactWriteTool{ws: ws}
}

// ToolDef returns the schema.ToolDef for the artifact_write tool.
func (t *ArtifactWriteTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ArtifactWriteToolName,
		Description: artifactWriteToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  artifactWriteParametersSchema,
	}
}

// Handler returns the ToolHandler closure for artifact_write.
func (t *ArtifactWriteTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "artifact_write: session id missing from context"), nil
		}

		var parsed struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "artifact_write: invalid arguments: "+err.Error()), nil
		}

		path, err := t.ws.WriteArtifact(ctx, sessionID, parsed.Name, []byte(parsed.Content))
		if err != nil {
			return schema.ErrorResult("", "artifact_write: "+err.Error()), nil
		}

		bytesWritten := len(parsed.Content)
		if em := schema.EmitterFromContext(ctx); em != nil {
			_ = em(schema.NewEvent(
				schema.EventWorkspaceArtifactWritten, "", sessionID,
				schema.WorkspaceArtifactWrittenData{
					SessionID: sessionID,
					Name:      parsed.Name,
					Bytes:     bytesWritten,
					Path:      path,
				},
			))
		}

		return schema.TextResult("", fmt.Sprintf("ok (%d bytes written to artifacts/%s)", bytesWritten, parsed.Name)), nil
	}
}

// ArtifactReadTool wires the artifact_read handler to a Workspace.
type ArtifactReadTool struct {
	ws workspace.Workspace
}

// NewArtifactReadTool constructs an ArtifactReadTool.
func NewArtifactReadTool(ws workspace.Workspace) *ArtifactReadTool {
	return &ArtifactReadTool{ws: ws}
}

// ToolDef returns the schema.ToolDef for the artifact_read tool.
func (t *ArtifactReadTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ArtifactReadToolName,
		Description: artifactReadToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  artifactReadParametersSchema,
	}
}

// Handler returns the ToolHandler closure for artifact_read.
func (t *ArtifactReadTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sessionID := schema.SessionIDFromContext(ctx)
		if sessionID == "" {
			return schema.ErrorResult("", "artifact_read: session id missing from context"), nil
		}

		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "artifact_read: invalid arguments: "+err.Error()), nil
		}

		data, err := t.ws.ReadArtifact(ctx, sessionID, parsed.Name)
		if err != nil {
			return schema.ErrorResult("", "artifact_read: "+err.Error()), nil
		}
		if data == nil {
			return schema.TextResult("", fmt.Sprintf("(no artifact %q)", parsed.Name)), nil
		}
		return schema.TextResult("", string(data)), nil
	}
}

// RegisterArtifacts creates the artifact_write and artifact_read tools
// bound to ws and registers them in reg. Returns an error on duplicate
// name or nil ws.
func RegisterArtifacts(reg *tool.Registry, ws workspace.Workspace) error {
	if ws == nil {
		return errors.New("workspace.RegisterArtifacts: workspace is nil")
	}
	wt := NewArtifactWriteTool(ws)
	if err := reg.RegisterIfAbsent(wt.ToolDef(), wt.Handler()); err != nil {
		return err
	}
	rt := NewArtifactReadTool(ws)
	return reg.RegisterIfAbsent(rt.ToolDef(), rt.Handler())
}
