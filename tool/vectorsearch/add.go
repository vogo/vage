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

package vectorsearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/vector"
)

const addToolDescription = `Write a text document into the vector memory store.

WHEN to use:
  - You discovered a stable fact you want future sessions to be able to recall.
  - You want to record a conclusion or decision tied to the current session for later search.

HOW to use:
  - Provide 'text' (required, ≤16 KiB). The text will be embedded and stored.
  - Optional 'id' lets you upsert a known document; omit to auto-generate.
  - Optional 'metadata' is a JSON object that will be stored with the document
    and is searchable via vector_search's 'metadata' filter
    (e.g. {"topic": "deployment", "session_id": "abc"}).

DO NOT:
  - Store ephemeral context (chat scratchpad, intermediate tool output) — that belongs to the session log, not long-term memory.
  - Stuff secrets / credentials into the document; the store is queryable.`

var addParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id": map[string]any{
			"type":        "string",
			"description": "Optional document id. Empty -> a random id is assigned.",
		},
		"text": map[string]any{
			"type":        "string",
			"description": "Document body to embed and store. Required.",
		},
		"metadata": map[string]any{
			"type":        "object",
			"description": "Optional metadata stored alongside the document.",
		},
	},
	"required": []string{"text"},
}

// MaxTextBytes caps the input length to keep token cost predictable
// and prevent the LLM from accidentally piping a giant blob into the
// store. 16 KiB is large enough for a several-paragraph note.
const MaxTextBytes = 16 * 1024

type addArgs struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata"`
}

type addTool struct {
	store    vector.VectorStore
	embedder vector.Embedder
}

func newAddTool(s vector.VectorStore, e vector.Embedder) *addTool {
	return &addTool{store: s, embedder: e}
}

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
		var parsed addArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", AddToolName+": invalid arguments: "+err.Error()), nil
		}
		text := strings.TrimSpace(parsed.Text)
		if text == "" {
			return schema.ErrorResult("", AddToolName+": 'text' is required"), nil
		}
		if len(text) > MaxTextBytes {
			return schema.ErrorResult("", fmt.Sprintf("%s: text exceeds %d bytes; trim or split", AddToolName, MaxTextBytes)), nil
		}

		id := strings.TrimSpace(parsed.ID)
		if id == "" {
			gen, err := newRandomID()
			if err != nil {
				return schema.ErrorResult("", AddToolName+": cannot allocate id: "+err.Error()), nil
			}
			id = gen
		}

		vec, err := t.embedder.Embed(ctx, text)
		if err != nil {
			return errResult(AddToolName, err), nil
		}

		// Tag with the session id from context when present so subsequent
		// vector_search filters can scope to the same session — matches
		// the auto-write hook's metadata shape.
		md := make(map[string]any, len(parsed.Metadata)+1)
		maps.Copy(md, parsed.Metadata)
		if sid := schema.SessionIDFromContext(ctx); sid != "" {
			if _, ok := md["session_id"]; !ok {
				md["session_id"] = sid
			}
		}

		doc := vector.Document{
			ID:        id,
			Text:      text,
			Embedding: vec,
			Metadata:  md,
		}
		if err := t.store.Add(ctx, doc); err != nil {
			return errResult(AddToolName, err), nil
		}
		return schema.TextResult("", fmt.Sprintf("ok (id=%s, bytes=%d)", id, len(text))), nil
	}
}

// newRandomID returns a 16-byte hex string. We avoid UUID format here —
// the qdrant backend derives its own UUIDv5 from this string, so any
// stable identifier suffices.
func newRandomID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
