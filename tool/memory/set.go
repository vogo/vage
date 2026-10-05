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
const SetToolDescription = `Write a durable fact into persistent memory.

WHEN to use — only when ALL hold: the user corrected an existing convention, stated a stable project/personal preference, or a failure's root cause is worth reusing across sessions.

WHEN NOT to use:
  - Never write process intermediates, tool traces, or one-off task state.

HOW:
  - namespace (required): shared names are project / user / conventions / notes / default (cross-session). Any other name is this session only.
  - key, value (required): the fact. Overwrite the same key to correct it. There is no delete tool.

Look up later with memory_recall.`

var setParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
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
			"description": "Fact body. Required. Max 16 KiB.",
		},
	},
	"required": []string{"namespace", "key", "value"},
}

type setArgs struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
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
		if !validIdent(ns) {
			return schema.ErrorResult("", SetToolName+": 'namespace' is required and must be a safe identifier"), nil
		}
		if !validIdent(key) {
			return schema.ErrorResult("", SetToolName+": 'key' is required and must be a safe identifier"), nil
		}
		if value == "" {
			return schema.ErrorResult("", SetToolName+": 'value' is required"), nil
		}
		if len(value) > MaxValueBytes {
			return schema.ErrorResult("", fmt.Sprintf("%s: value exceeds %d bytes; trim or split", SetToolName, MaxValueBytes)), nil
		}

		lk := logicalKey(ns, key)
		if err := t.store.Set(ctx, lk, value, 0); err != nil {
			return errResult(SetToolName, err), nil
		}

		shared := IsSharedNamespace(ns)
		schema.EmitCustomData(ctx, EventSet, map[string]any{
			"namespace": ns,
			"key":       key,
			"shared":    shared,
		})

		scope := "session-private"
		if shared {
			scope = "shared"
		}
		return schema.TextResult("", fmt.Sprintf("ok (namespace=%s key=%s scope=%s)", ns, key, scope)), nil
	}
}
