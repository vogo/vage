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

// SetToolDescription is the LLM-facing contract for memory_set. Markers
// in this text must stay in lockstep with the host Primary system prompt
// (when-to-write gate).
const SetToolDescription = `Write or delete one durable fact in persistent memory.

WHEN to use — only when ALL hold: the user corrected an existing convention, stated a stable project/personal preference, or a failure's root cause is worth reusing across sessions. Use op=delete to drop a single key that is no longer true.

WHEN NOT to use:
  - Never write process intermediates, tool traces, or one-off task state.
  - Never clear a namespace or the whole store. This tool deletes one key only.

HOW:
  - op: set (default) or delete.
  - namespace (required): shared names are project / user / conventions / notes / default (cross-session). Any other name is this session only.
  - key (required).
  - value: required for set. Must be omitted or empty for delete. Max 16 KiB.
  - ttl: optional seconds until a set expires. 0 or omitted means no expiry. Ignored for delete.

Look up later with memory_recall.`

var setParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"op": map[string]any{
			"type":        "string",
			"description": "set (default) writes the fact. delete removes one key; value must be omitted or empty.",
			"enum":        []string{"set", "delete"},
		},
		"namespace": map[string]any{
			"type":        "string",
			"description": "Memory namespace. Shared: project/user/conventions/notes/default. Any other name is session-private.",
		},
		"key": map[string]any{
			"type":        "string",
			"description": "Fact key within the namespace. Letters, digits, '-', '_', '.'.",
		},
		"value": map[string]any{
			"type":        "string",
			"description": "Fact body. Required for op=set. Must be omitted or empty for op=delete. Max 16 KiB.",
		},
		"ttl": map[string]any{
			"type":        "integer",
			"description": "Seconds until op=set expires. 0 or omitted means no expiry. Ignored for delete.",
		},
	},
	"required": []string{"namespace", "key"},
}

type setArgs struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Op        string `json:"op"`
	TTL       int64  `json:"ttl"`
}

type setTool struct {
	store Store
}

func newSetTool(s Store) *setTool {
	return &setTool{store: s}
}

func (t *setTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        SetToolName,
		Description: SetToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true, // not a workspace-file mutation; durable KV only
		Parameters:  setParametersSchema,
	}
}

func (t *setTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed setArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", SetToolName+": invalid arguments: "+err.Error()), nil
		}
		ns := strings.TrimSpace(parsed.Namespace)
		key := strings.TrimSpace(parsed.Key)
		value := strings.TrimSpace(parsed.Value)
		op := strings.TrimSpace(parsed.Op)
		if op == "" {
			op = "set"
		}
		if op != "set" && op != "delete" {
			return schema.ErrorResult("", SetToolName+": 'op' must be set or delete"), nil
		}
		if parsed.TTL < 0 {
			return schema.ErrorResult("", SetToolName+": 'ttl' must not be negative"), nil
		}
		if !validIdent(ns) {
			return schema.ErrorResult("", SetToolName+": 'namespace' is required and must be a safe identifier"), nil
		}
		if !validIdent(key) {
			return schema.ErrorResult("", SetToolName+": 'key' is required and must be a safe identifier"), nil
		}

		lk := logicalKey(ns, key)
		shared := IsSharedNamespace(ns)
		scope := "session-private"
		if shared {
			scope = "shared"
		}

		if op == "delete" {
			if value != "" {
				return schema.ErrorResult("", SetToolName+": 'value' must be omitted or empty for delete"), nil
			}
			if err := t.store.Delete(ctx, lk); err != nil {
				return errResult(SetToolName, err), nil
			}
			schema.EmitCustomData(ctx, EventDelete, map[string]any{
				"namespace": ns,
				"key":       key,
				"shared":    shared,
			})
			return schema.TextResult("", fmt.Sprintf("ok (op=delete namespace=%s key=%s scope=%s)", ns, key, scope)), nil
		}

		if value == "" {
			return schema.ErrorResult("", SetToolName+": 'value' is required"), nil
		}
		if len(value) > MaxValueBytes {
			return schema.ErrorResult("", fmt.Sprintf("%s: value exceeds %d bytes; trim or split", SetToolName, MaxValueBytes)), nil
		}
		if err := t.store.Set(ctx, lk, value, parsed.TTL); err != nil {
			return errResult(SetToolName, err), nil
		}

		schema.EmitCustomData(ctx, EventSet, map[string]any{
			"namespace": ns,
			"key":       key,
			"shared":    shared,
		})
		return schema.TextResult("", fmt.Sprintf("ok (namespace=%s key=%s scope=%s)", ns, key, scope)), nil
	}
}
