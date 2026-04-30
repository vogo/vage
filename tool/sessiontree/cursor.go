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

const cursorToolDescription = `Move the SessionTree cursor to a node.

WHEN to use:
  - You decided which sub-task to focus on next; pin attention by moving the cursor.
  - You finished a sub-task and want to step back to its parent before picking the next.

HOW to use:
  - Pass node_id of the node to focus on.
  - Pass node_id="" to clear the cursor (rare; usually only on session wrap-up).

DO NOT:
  - Bounce the cursor around mid-step; the tree's "where am I" signal is most useful when stable.`

var cursorParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"node_id": map[string]any{
			"type":        "string",
			"description": "Target node id, or empty string to clear the cursor.",
		},
	},
	"required": []string{"node_id"},
}

type cursorArgs struct {
	NodeID string `json:"node_id"`
}

type cursorTool struct {
	store tree.SessionTreeStore
}

func newCursorTool(s tree.SessionTreeStore) *cursorTool { return &cursorTool{store: s} }

func (t *cursorTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        CursorToolName,
		Description: cursorToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  cursorParametersSchema,
	}
}

func (t *cursorTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sid, errRes := requireSession(CursorToolName, schema.SessionIDFromContext(ctx))
		if errRes != nil {
			return *errRes, nil
		}

		var parsed cursorArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", CursorToolName+": invalid arguments: "+err.Error()), nil
		}

		if err := t.store.SetCursor(ctx, sid, parsed.NodeID); err != nil {
			return errResult(CursorToolName, err), nil
		}

		var msg string
		if parsed.NodeID == "" {
			msg = "ok (cursor cleared)"
		} else {
			msg = fmt.Sprintf("ok (cursor → %s)", parsed.NodeID)
		}
		return schema.TextResult("", msg), nil
	}
}
