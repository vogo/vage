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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/vector"
)

const searchToolDescription = `Search the vector memory store for documents similar to a query.

WHEN to use:
  - You need background context that may have been recorded in past sessions.
  - You want to confirm whether a fact, decision, or artifact has already been captured.

HOW to use:
  - Pass 'query' as the natural-language text you want to find similar documents to.
  - Optional 'top_k' bounds the number of hits returned (default 5).
  - Optional 'min_score' filters out hits below the threshold (cosine similarity 0..1).
  - Optional 'metadata' is a JSON object of equality filters (e.g. {"session_id": "abc"}).

DO NOT:
  - Use this tool to retrieve a document by exact id; the store is similarity-keyed.
  - Pass a long document body as the query — the embedder will truncate; pass a focused phrase.`

var searchParametersSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "Natural-language query text. Required.",
		},
		"top_k": map[string]any{
			"type":        "integer",
			"description": "Maximum number of hits to return. Default 5; range 1..50.",
		},
		"min_score": map[string]any{
			"type":        "number",
			"description": "Optional lower bound on cosine similarity (0..1).",
		},
		"metadata": map[string]any{
			"type":        "object",
			"description": "Optional equality filter on document metadata. Keys are matched exactly.",
		},
	},
	"required": []string{"query"},
}

// Hard cap so a runaway tool call does not exhaust the budget. 50 is
// generous — production callers typically see useful signal under 10.
const maxTopK = 50

type searchArgs struct {
	Query    string         `json:"query"`
	TopK     int            `json:"top_k"`
	MinScore float32        `json:"min_score"`
	Metadata map[string]any `json:"metadata"`
}

type searchTool struct {
	store    vector.VectorStore
	embedder vector.Embedder
}

func newSearchTool(s vector.VectorStore, e vector.Embedder) *searchTool {
	return &searchTool{store: s, embedder: e}
}

func (t *searchTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        SearchToolName,
		Description: searchToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters:  searchParametersSchema,
	}
}

func (t *searchTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed searchArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", SearchToolName+": invalid arguments: "+err.Error()), nil
		}
		if parsed.Query == "" {
			return schema.ErrorResult("", SearchToolName+": 'query' is required"), nil
		}
		if parsed.TopK <= 0 {
			parsed.TopK = vector.DefaultTopK
		}
		if parsed.TopK > maxTopK {
			parsed.TopK = maxTopK
		}

		vec, err := t.embedder.Embed(ctx, parsed.Query)
		if err != nil {
			return errResult(SearchToolName, err), nil
		}
		hits, err := t.store.Search(ctx, vec, vector.SearchOptions{
			TopK:           parsed.TopK,
			MinScore:       parsed.MinScore,
			MetadataEquals: parsed.Metadata,
		})
		if err != nil {
			return errResult(SearchToolName, err), nil
		}

		return schema.TextResult("", formatHits(hits)), nil
	}
}

// formatHits renders a numbered, score-tagged list. The shape mirrors
// vctx.defaultHitsRender but adds the ID so the LLM can chain a
// follow-up vector_add referencing the same id.
func formatHits(hits []vector.SearchHit) string {
	if len(hits) == 0 {
		return "no hits"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hit(s):\n", len(hits))
	for i, h := range hits {
		text := strings.TrimSpace(h.Document.Text)
		fmt.Fprintf(&b, "%d. id=%s score=%.3f\n   %s\n", i+1, h.Document.ID, h.Score, summarizeOneLine(text))
	}
	return b.String()
}

// summarizeOneLine collapses whitespace and clips at 240 bytes so a
// single huge document does not dominate the tool result. The LLM can
// always vector_search again with a more specific query.
func summarizeOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 240
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
