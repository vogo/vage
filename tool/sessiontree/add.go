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
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/tool"
)

const addToolDescription = `Add a node to the SessionTree of the current session.

WHEN to use:
  - You are decomposing a task into sub-tasks; capture each as a subtask node.
  - You discovered a stable fact you want the next turn to remember.
  - The first call on an empty tree creates the root (goal) node automatically.

HOW to use:
  - Pass parent_id="" to create the root (or to attach to root when one exists).
  - Choose a 'type': "goal" (root only), "subtask", "fact", "observation", or "artifact_ref".
  - Title is required (≤200 bytes). Summary is optional (≤2 KiB).

DO NOT:
  - Use this tool for ephemeral todos within a single ReAct loop — that is todo_write.
  - Stuff long content into 'summary' — point to a workspace note via content_ref instead.`

var addParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"parent_id": map[string]any{
			"type":        "string",
			"description": "Parent node id; empty creates the root or attaches to the existing root.",
		},
		"type": map[string]any{
			"type":        "string",
			"enum":        []string{"goal", "subtask", "fact", "observation", "artifact_ref"},
			"description": "Node type. Default 'subtask'. Use 'goal' only for the root.",
		},
		"title": map[string]any{
			"type":        "string",
			"description": "Short headline (≤200 bytes). Required.",
		},
		"summary": map[string]any{
			"type":        "string",
			"description": "Optional longer description (≤2 KiB).",
		},
		"status": map[string]any{
			"type":        "string",
			"enum":        []string{"pending", "active", "done", "blocked", "superseded"},
			"description": "Default 'pending' for non-root nodes, 'active' for root.",
		},
		"pinned": map[string]any{
			"type":        "boolean",
			"description": "When true, the node is excluded from automatic promotion / folding.",
		},
		"content_ref": map[string]any{
			"type":        "string",
			"description": "Optional pointer to a workspace note or artifact for richer detail.",
		},
	},
	"required": []string{"title"},
}

type addArgs struct {
	ParentID   string `json:"parent_id"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Status     string `json:"status"`
	Pinned     bool   `json:"pinned"`
	ContentRef string `json:"content_ref"`
}

type addTool struct {
	store tree.SessionTreeStore
}

func newAddTool(s tree.SessionTreeStore) *addTool { return &addTool{store: s} }

func (t *addTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        AddToolName,
		Description: addToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  addParametersSchema,
	}
}

func (t *addTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sid, errRes := requireSession(AddToolName, schema.SessionIDFromContext(ctx))
		if errRes != nil {
			return *errRes, nil
		}

		var parsed addArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", AddToolName+": invalid arguments: "+err.Error()), nil
		}

		nodeType := tree.NodeType(parsed.Type)
		if nodeType == "" {
			nodeType = tree.NodeSubtask
		}
		status := tree.NodeStatus(parsed.Status)

		newNode := tree.TreeNode{
			Type:       nodeType,
			Status:     status,
			Title:      parsed.Title,
			Summary:    parsed.Summary,
			Pinned:     parsed.Pinned,
			ContentRef: parsed.ContentRef,
		}

		// parent_id == "" means "attach to root, or create root if missing".
		if parsed.ParentID == "" {
			out, err := t.attachToRoot(ctx, sid, newNode)
			if err != nil {
				return errResult(AddToolName, err), nil
			}
			return successAdd(out, "root"), nil
		}

		out, err := t.store.AddNode(ctx, sid, parsed.ParentID, newNode)
		if err != nil {
			return errResult(AddToolName, err), nil
		}
		return successAdd(out, parsed.ParentID), nil
	}
}

// attachToRoot creates the tree when missing and otherwise appends the node
// under the existing root. Behaviour matches "parent_id is the root by
// default" — the simplest mental model for an LLM that does not yet hold a
// node id.
func (t *addTool) attachToRoot(ctx context.Context, sid string, n tree.TreeNode) (*tree.TreeNode, error) {
	tr, err := t.store.GetTree(ctx, sid)
	switch {
	case err == nil:
		return t.store.AddNode(ctx, sid, tr.RootID, n)
	case errors.Is(err, tree.ErrTreeMissing):
		// Promote the supplied node to root: type goal + status active by default.
		root := n
		if root.Type == "" || root.Type == tree.NodeSubtask {
			root.Type = tree.NodeGoal
		}
		if root.Status == "" {
			root.Status = tree.StatusActive
		}
		return t.store.CreateTree(ctx, sid, root)
	default:
		return nil, err
	}
}

func successAdd(n *tree.TreeNode, parentID string) schema.ToolResult {
	if n == nil {
		return schema.ErrorResult("", AddToolName+": store returned nil node")
	}
	msg := fmt.Sprintf("ok (added node %s under %s, depth=%d, type=%s)",
		n.ID, parentID, n.Depth, n.Type)
	return schema.TextResult("", msg)
}
