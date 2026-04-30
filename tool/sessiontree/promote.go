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

const promoteToolDescription = `Fold a node's eligible children into the node itself.

WHEN to use:
  - You finished all child sub-tasks of a node and want to compress them into the node's summary.
  - The node has so many children that the prompt no longer fits — fold to free budget.

HOW to use:
  - Pass node_id of the parent. The system aggregates non-pinned, non-promoted children into the parent's new summary using the configured promoter (LLM / compressor / noop).
  - Folded children stay in the tree (audit trail) but disappear from the default render. Use tree_zoom_in to inspect them.

DO NOT:
  - Promote nodes you may still be working on — lookup is correct but the rendered tree gets noisier and folding hides progress.
  - Loop tree_promote tightly; the system already triggers automatic promotion on thresholds.`

var promoteParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"node_id": map[string]any{
			"type":        "string",
			"description": "Parent node id whose children should be folded.",
		},
	},
	"required": []string{"node_id"},
}

type promoteArgs struct {
	NodeID string `json:"node_id"`
}

type promoteTool struct {
	store tree.SessionTreeStore
}

func newPromoteTool(s tree.SessionTreeStore) *promoteTool { return &promoteTool{store: s} }

func (t *promoteTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        PromoteToolName,
		Description: promoteToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    false,
		Parameters:  promoteParametersSchema,
	}
}

func (t *promoteTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sid, errRes := requireSession(PromoteToolName, schema.SessionIDFromContext(ctx))
		if errRes != nil {
			return *errRes, nil
		}

		var parsed promoteArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", PromoteToolName+": invalid arguments: "+err.Error()), nil
		}

		out, err := t.store.PromoteNode(ctx, sid, parsed.NodeID)
		if err != nil {
			return errResult(PromoteToolName, err), nil
		}

		summaryBytes := 0
		if out != nil {
			summaryBytes = len(out.Summary)
		}
		// folded count cannot be derived from PromoteNode return shape; the
		// store dispatches a structured Completed event with the precise
		// number. Surface a brief acknowledgement so the LLM can move on.
		msg := fmt.Sprintf("ok (promoted %s, new summary=%d bytes)", parsed.NodeID, summaryBytes)
		return schema.TextResult("", msg), nil
	}
}
