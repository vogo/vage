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

package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vogo/largemodel/schema"
	"github.com/vogo/vage/tool"
)

const RecallToolDescription = `Recall durable facts from persistent memory by exact namespace / key prefix.

WHEN to use:
  - You need a project convention, user preference, or previously recorded pitfall and do not have it in the current prompt.
  - After memory_set, to confirm what was stored.

HOW:
  - namespace (optional): empty means every shared namespace (project/user/conventions/notes/default). Name a session-private namespace explicitly to read it.
  - prefix (optional): filters the key (the name inside the namespace), not the namespace itself.
  - limit (optional): default 20, hard cap 50.

No match is success with an empty list, not an error. This is exact lookup, not vector search.`

var recallParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"namespace": map[string]any{
			"type":        "string",
			"description": "Optional namespace filter. Empty = all shared namespaces.",
		},
		"prefix": map[string]any{
			"type":        "string",
			"description": "Optional key-name prefix within the selected namespace(s).",
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Maximum entries to return. Default 20; range 1..50.",
		},
	},
}

type recallArgs struct {
	Namespace string `json:"namespace"`
	Prefix    string `json:"prefix"`
	Limit     int    `json:"limit"`
}

type recallTool struct {
	store Store
}

func newRecallTool(s Store) *recallTool {
	return &recallTool{store: s}
}

func (t *recallTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        RecallToolName,
		Description: RecallToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  recallParametersSchema,
	}
}

func (t *recallTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed recallArgs
		if args != "" {
			if err := json.Unmarshal([]byte(args), &parsed); err != nil {
				return schema.ErrorResult("", RecallToolName+": invalid arguments: "+err.Error()), nil
			}
		}
		ns := strings.TrimSpace(parsed.Namespace)
		prefix := strings.TrimSpace(parsed.Prefix)
		if ns != "" && !validIdent(ns) {
			return schema.ErrorResult("", RecallToolName+": 'namespace' must be a safe identifier"), nil
		}
		if prefix != "" && !validIdent(prefix) {
			return schema.ErrorResult("", RecallToolName+": 'prefix' must be a safe identifier"), nil
		}
		limit := parsed.Limit
		if limit <= 0 {
			limit = DefaultLimit
		}
		if limit > MaxLimit {
			limit = MaxLimit
		}

		listPrefix := ""
		if ns != "" {
			listPrefix = logicalKey(ns, prefix)
		}

		entries, err := t.store.List(ctx, listPrefix)
		if err != nil {
			return errResult(RecallToolName, err), nil
		}

		filtered := make([]Entry, 0, len(entries))
		for i := range entries {
			e := entries[i]
			ens, ename := splitLogicalKey(e.Key)
			if ns == "" {
				if !IsSharedNamespace(ens) {
					continue
				}
				if prefix != "" && !strings.HasPrefix(ename, prefix) {
					continue
				}
			} else if ens != ns {
				continue
			} else if prefix != "" && !strings.HasPrefix(ename, prefix) {
				continue
			}
			filtered = append(filtered, e)
		}
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}

		return schema.TextResult("", formatEntries(filtered)), nil
	}
}

func formatEntries(entries []Entry) string {
	if len(entries) == 0 {
		return "no entries"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entries:\n", len(entries))
	for i, e := range entries {
		ns, name := splitLogicalKey(e.Key)
		fmt.Fprintf(&b, "%d. %s:%s\n   %s\n", i+1, ns, name, clipOneLine(valueString(e.Value)))
	}
	return b.String()
}
