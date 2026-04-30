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

package sessiontree

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/tool"
)

const updateToolDescription = `Update a SessionTree node's mutable fields.

WHEN to use:
  - You finished a step and want to mark its node 'done'.
  - You learned the title or summary you wrote earlier is wrong / can be improved.
  - You want to pin a node so it survives automatic folding.

HOW to use:
  - Pass node_id (a "tn-..." id you saw earlier from tree_add or the rendered tree).
  - Title is required (treat update as a 'rewrite' — pass the current title verbatim if unchanged).
  - Type and parent are immutable; reshape the tree by adding new nodes and marking the old one 'superseded'.

DO NOT:
  - Use this for delete — that is tree_zoom_in's read-only flow not provided; use the HTTP endpoint or wait for tree_delete.
  - Set Promoted manually unless you are reverting an automatic fold.`

var updateParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"node_id": map[string]any{
			"type":        "string",
			"description": "Node id to update (e.g., 'tn-...').",
		},
		"title": map[string]any{
			"type":        "string",
			"description": "Required. Pass the current title unchanged when only updating other fields.",
		},
		"summary": map[string]any{
			"type":        "string",
			"description": "Optional summary text (≤2 KiB).",
		},
		"status": map[string]any{
			"type":        "string",
			"enum":        []string{"pending", "active", "done", "blocked", "superseded"},
			"description": "Optional status update.",
		},
		"pinned": map[string]any{
			"type":        "boolean",
			"description": "Optional pin flag (defaults to current value when omitted).",
		},
		"content_ref": map[string]any{
			"type":        "string",
			"description": "Optional content_ref update.",
		},
	},
	"required": []string{"node_id", "title"},
}

type updateArgs struct {
	NodeID     string  `json:"node_id"`
	Title      string  `json:"title"`
	Summary    *string `json:"summary"`
	Status     string  `json:"status"`
	Pinned     *bool   `json:"pinned"`
	ContentRef *string `json:"content_ref"`
}

type updateTool struct {
	store tree.SessionTreeStore
}

func newUpdateTool(s tree.SessionTreeStore) *updateTool { return &updateTool{store: s} }

func (t *updateTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        UpdateToolName,
		Description: updateToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  updateParametersSchema,
	}
}

func (t *updateTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sid, errRes := requireSession(UpdateToolName, schema.SessionIDFromContext(ctx))
		if errRes != nil {
			return *errRes, nil
		}

		var parsed updateArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", UpdateToolName+": invalid arguments: "+err.Error()), nil
		}

		// Read the current node so omitted fields keep their values. Reads
		// are deep-copied by the store so we can mutate freely.
		tr, err := t.store.GetTree(ctx, sid)
		if err != nil {
			return errResult(UpdateToolName, err), nil
		}
		cur, ok := tr.Nodes[parsed.NodeID]
		if !ok {
			return schema.ErrorResult("", fmt.Sprintf("%s: node not found: %q", UpdateToolName, parsed.NodeID)), nil
		}

		next := *cur
		next.Title = parsed.Title
		if parsed.Summary != nil {
			next.Summary = *parsed.Summary
		}
		if parsed.Status != "" {
			next.Status = tree.NodeStatus(parsed.Status)
		}
		if parsed.Pinned != nil {
			next.Pinned = *parsed.Pinned
		}
		if parsed.ContentRef != nil {
			next.ContentRef = *parsed.ContentRef
		}
		// Type and Parent must NOT be set on update; the store rejects
		// mismatches but we strip them here anyway to avoid confusion when
		// the user-supplied JSON re-stamps a stale value through the get.
		next.Type = ""
		next.Parent = ""

		out, err := t.store.UpdateNode(ctx, sid, next)
		if err != nil {
			return errResult(UpdateToolName, err), nil
		}

		msg := fmt.Sprintf("ok (updated %s, status=%s)", out.ID, out.Status)
		return schema.TextResult("", msg), nil
	}
}
