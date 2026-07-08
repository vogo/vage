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

// Package sessiontree implements the LLM-facing tools that read and write the
// per-session SessionTree (vage/session/tree). It is intentionally narrow:
// tree_add / tree_update / tree_cursor / tree_promote / tree_zoom_in with
// strict argument validation, no path arguments, and structured event
// emission delegated to the underlying SessionTreeStore.
package sessiontree

import (
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/tool"
)

// Tool names. Kept as exported constants so callers (registry, tests, and
// the tool registry owner's permission gate) reference one identifier.
const (
	AddToolName     = "tree_add"
	UpdateToolName  = "tree_update"
	CursorToolName  = "tree_cursor"
	PromoteToolName = "tree_promote"
	ZoomInToolName  = "tree_zoom_in"
)

// errStoreRequired is returned by Register when the supplied store is nil.
// Tools never partially install — either all five succeed or Register fails.
var errStoreRequired = errors.New("sessiontree: tree store is required")

// Register installs all five tree tools onto reg, bound to store. Returns
// an error on duplicate name or nil store. The function is "all-or-nothing"
// for the LLM contract: a partial registration would let the model see
// tree_add but not tree_promote and silently break promotion flows.
func Register(reg *tool.Registry, store tree.SessionTreeStore) error {
	if store == nil {
		return errStoreRequired
	}
	if reg == nil {
		return errors.New("sessiontree: registry is nil")
	}

	add := newAddTool(store)
	if err := reg.RegisterIfAbsent(add.ToolDef(), add.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", AddToolName, err)
	}
	upd := newUpdateTool(store)
	if err := reg.RegisterIfAbsent(upd.ToolDef(), upd.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", UpdateToolName, err)
	}
	cur := newCursorTool(store)
	if err := reg.RegisterIfAbsent(cur.ToolDef(), cur.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", CursorToolName, err)
	}
	pro := newPromoteTool(store)
	if err := reg.RegisterIfAbsent(pro.ToolDef(), pro.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", PromoteToolName, err)
	}
	zoom := newZoomInTool(store)
	if err := reg.RegisterIfAbsent(zoom.ToolDef(), zoom.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", ZoomInToolName, err)
	}
	return nil
}

// errResult formats err into a tool-error result, mapping known sentinels to
// a stable message prefix so the LLM can react. Unknown errors fall through
// unchanged.
func errResult(toolName string, err error) schema.ToolResult {
	switch {
	case errors.Is(err, tree.ErrInvalidArgument):
		return schema.ErrorResult("", toolName+": invalid argument: "+err.Error())
	case errors.Is(err, tree.ErrNotFound):
		return schema.ErrorResult("", toolName+": not found: "+err.Error())
	case errors.Is(err, tree.ErrTreeMissing):
		return schema.ErrorResult("", toolName+": tree does not exist; create it first by calling tree_add with parent_id=\"\"")
	case errors.Is(err, tree.ErrAlreadyExists):
		return schema.ErrorResult("", toolName+": tree already exists")
	case errors.Is(err, tree.ErrTreeFull):
		return schema.ErrorResult("", toolName+": tree is full; promote or delete nodes first")
	case errors.Is(err, tree.ErrHasChildren):
		return schema.ErrorResult("", toolName+": node has children; delete leaves first")
	case errors.Is(err, tree.ErrImmutableField):
		return schema.ErrorResult("", toolName+": field is immutable: "+err.Error())
	default:
		return schema.ErrorResult("", toolName+": "+err.Error())
	}
}

// requireSession reads the session id from ctx; returns ("", err-result) when
// the id is missing — the agent runtime injects it via schema.WithSessionID
// before every tool call.
func requireSession(toolName string, sessionID string) (string, *schema.ToolResult) {
	if sessionID == "" {
		r := schema.ErrorResult("", toolName+": session id missing from context")
		return "", &r
	}
	return sessionID, nil
}
