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

// Package vectorsearch implements two LLM-facing tools:
//
//   - vector_search — query the configured vector.VectorStore by text;
//   - vector_add    — write a text + metadata document into the store.
//
// Both tools embed text via the shared vector.Embedder so the LLM never
// hands raw vectors. They are intentionally narrow — no batch shapes, no
// cursor/scroll — to keep the schema small and the JSON args
// roundtripable. Bulk operations belong on the HTTP surface.
package vectorsearch

import (
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/vector"
)

// Tool names. Stable identifiers used by registries, permission gates,
// and (eventually) skill manifests.
const (
	SearchToolName = "vector_search"
	AddToolName    = "vector_add"
)

// errStoreRequired and errEmbedderRequired are surface-level sentinels.
// Register fails fast rather than producing a partial install, mirroring
// sessiontree.Register.
var (
	errStoreRequired    = errors.New("vectorsearch: store is required")
	errEmbedderRequired = errors.New("vectorsearch: embedder is required")
	errRegistryNil      = errors.New("vectorsearch: registry is nil")
)

// Register installs both vector tools onto reg. All-or-nothing: a
// partial registration would let the LLM see vector_search but not
// vector_add, encouraging it to invent recall round-trips it cannot
// finish.
func Register(reg *tool.Registry, store vector.VectorStore, embedder vector.Embedder) error {
	if reg == nil {
		return errRegistryNil
	}
	if store == nil {
		return errStoreRequired
	}
	if embedder == nil {
		return errEmbedderRequired
	}

	st := newSearchTool(store, embedder)
	if err := reg.RegisterIfAbsent(st.ToolDef(), st.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", SearchToolName, err)
	}
	at := newAddTool(store, embedder)
	if err := reg.RegisterIfAbsent(at.ToolDef(), at.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", AddToolName, err)
	}
	return nil
}

// errResult maps a backend error to an LLM-facing tool result. Known
// vector sentinels get stable prefixes so the model can recover; other
// errors pass through verbatim.
func errResult(toolName string, err error) schema.ToolResult {
	switch {
	case errors.Is(err, vector.ErrEmptyQuery):
		return schema.ErrorResult("", toolName+": query/text is empty")
	case errors.Is(err, vector.ErrDimensionMismatch):
		return schema.ErrorResult("", toolName+": dimension mismatch — embedder and store disagree on vector size")
	case errors.Is(err, vector.ErrNotFound):
		return schema.ErrorResult("", toolName+": not found")
	case errors.Is(err, vector.ErrNotSupported):
		return schema.ErrorResult("", toolName+": operation not supported by the configured backend")
	default:
		return schema.ErrorResult("", toolName+": "+err.Error())
	}
}
