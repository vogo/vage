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
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/tool"
)

const zoomInToolDescription = `Render the subtree rooted at a node, including folded children.

WHEN to use:
  - The default rendered tree shows "(folded: N children, M done)" and you want to read the originals.
  - You are reviewing past work captured in a promoted subtree.
  - Pass node_id="" to use the current cursor as the root.

HOW to use:
  - Returns a Markdown outline of the subtree. Promoted nodes are NOT skipped here — that is the point.

DO NOT:
  - Use this as a substitute for the prompt-injected tree; this is for ad-hoc zoom-in only.`

var zoomInParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"node_id": map[string]any{
			"type":        "string",
			"description": "Subtree root id; empty uses the current cursor.",
		},
		"max_depth": map[string]any{
			"type":        "integer",
			"description": "Optional cap on subtree depth (default 4).",
		},
	},
}

type zoomInArgs struct {
	NodeID   string `json:"node_id"`
	MaxDepth int    `json:"max_depth"`
}

type zoomInTool struct {
	store tree.SessionTreeStore
}

func newZoomInTool(s tree.SessionTreeStore) *zoomInTool { return &zoomInTool{store: s} }

func (t *zoomInTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        ZoomInToolName,
		Description: zoomInToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  zoomInParametersSchema,
	}
}

const (
	zoomInDefaultMaxDepth = 4
	// zoomInMaxBytes caps the rendered output at 16 KiB so a fat subtree
	// cannot starve the prompt budget on accidental deep zooms.
	zoomInMaxBytes = 16 * 1024
)

func (t *zoomInTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		sid, errRes := requireSession(ZoomInToolName, schema.SessionIDFromContext(ctx))
		if errRes != nil {
			return *errRes, nil
		}

		var parsed zoomInArgs
		if args != "" {
			if err := json.Unmarshal([]byte(args), &parsed); err != nil {
				return schema.ErrorResult("", ZoomInToolName+": invalid arguments: "+err.Error()), nil
			}
		}
		maxDepth := parsed.MaxDepth
		if maxDepth <= 0 {
			maxDepth = zoomInDefaultMaxDepth
		}

		// IncludePromoted=true: zoom-in is precisely the channel for inspecting
		// folded subtrees.
		tr, err := t.store.GetTreeView(ctx, sid, tree.ViewOptions{IncludePromoted: true})
		if err != nil {
			return errResult(ZoomInToolName, err), nil
		}

		rootID := parsed.NodeID
		if rootID == "" {
			rootID = tr.Cursor
			if rootID == "" {
				rootID = tr.RootID
			}
		}
		root, ok := tr.Nodes[rootID]
		if !ok {
			return schema.ErrorResult("", fmt.Sprintf("%s: node not found: %q", ZoomInToolName, rootID)), nil
		}

		text := renderSubtree(tr, root, maxDepth)
		if len(text) > zoomInMaxBytes {
			text = text[:zoomInMaxBytes] + "\n... [truncated]\n"
		}
		return schema.TextResult("", text), nil
	}
}

// renderSubtree walks tr from root, rendering up to maxDepth levels (root is
// depth 0). Each node prints on one line plus an indented summary when set.
// Promoted nodes are marked "[folded]" so the reader can tell.
func renderSubtree(tr *tree.SessionTree, root *tree.TreeNode, maxDepth int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Subtree at %s\n\n", root.ID)
	walkSubtree(&b, tr, root, 0, maxDepth)
	return b.String()
}

func walkSubtree(b *strings.Builder, tr *tree.SessionTree, n *tree.TreeNode, depth, maxDepth int) {
	indent := strings.Repeat("  ", depth)
	marker := ""
	if n.Promoted {
		marker = " [folded]"
	} else if n.Pinned {
		marker = " [pinned]"
	}
	fmt.Fprintf(b, "%s- [%s] [%s] %s%s (id=%s)\n", indent, n.Type, n.Status, n.Title, marker, n.ID)
	if n.Summary != "" {
		fmt.Fprintf(b, "%s  Summary: %s\n", indent, n.Summary)
	}
	if depth >= maxDepth {
		if len(n.Children) > 0 {
			fmt.Fprintf(b, "%s  (... %d more children below depth %d)\n", indent, len(n.Children), maxDepth)
		}
		return
	}
	for _, cid := range n.Children {
		c, ok := tr.Nodes[cid]
		if !ok {
			continue
		}
		walkSubtree(b, tr, c, depth+1, maxDepth)
	}
}
