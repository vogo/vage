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

package tool

// ResourceTracker is implemented by tools that read or write identifiable
// resources (files, db rows, network endpoints, etc.). Consumers — most
// notably largemodel.ContextEditorMiddleware's stale_resource pass — call
// ResourceIDs to learn which canonical resources a tool invocation will
// touch and whether the access is a read or a write. Tools opt in by
// implementing the interface; consumers detect support via type assertion
// so the contract is purely additive.
//
// Implementations MUST be tolerant of malformed or missing args: a tool
// must return nil rather than panic when args do not match the expected
// schema (e.g. wrong type, missing required field). Returning nil simply
// means "this invocation contributes no tracked resource" — the editor
// then leaves the corresponding tool_result untouched by the resource
// pass.
//
// Note: ResourceTracker is unrelated to toolkit.ReadTracker. The latter
// records "which paths this agent has already read" for read-before-edit
// safety. ResourceTracker exposes "which resources this single invocation
// touches and how" for context editing.
type ResourceTracker interface {
	// ResourceIDs returns the canonical IDs touched by this invocation.
	// args is the same map[string]any an executor would obtain by
	// json.Unmarshal-ing the raw arguments string the LLM produced.
	// Returns nil for invocations that don't touch any tracked resource.
	ResourceIDs(args map[string]any) []ResourceRef
}

// ResourceRef identifies one resource touched by a tool invocation,
// together with the access mode (read vs write). The zero value is
// legal but meaningless; callers should ignore refs with empty IDs.
type ResourceRef struct {
	// ID is the canonical resource identifier. For file-backed tools
	// implementations should pass the path through filepath.Clean so
	// that "./a/b" and "a/b" compare equal. Tools backed by other
	// systems (e.g. databases) are free to use their own canonical
	// form provided the encoding is stable across invocations.
	ID string `json:"id"`

	// Mode indicates whether the invocation reads or writes the
	// resource. Stale-resource detection treats writes as
	// invalidating any earlier read of the same ID.
	Mode ResourceMode `json:"mode"`
}

// ResourceMode is the access mode reported by ResourceRef. Only the two
// constants below are valid; consumers should treat any other value as
// "unknown" and skip the ref.
type ResourceMode string

// Resource access modes recognised by ContextEditorMiddleware. The
// string values are part of the public contract: they are emitted in
// debug logs and may appear in event payloads.
const (
	ResourceRead  ResourceMode = "read"
	ResourceWrite ResourceMode = "write"
)
