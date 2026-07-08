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

package largemodel

import "fmt"

// This file isolates the V1 placeholder compatibility layer from the
// context editor hot path. The legacy PlaceholderFunc / WithPlaceholder
// API and the V1↔V2 template selection live here so the main middleware
// only calls the unified renderPlaceholder helper and never carries the
// deprecated wire-format probing inline. Nothing here changes the wire
// format; it is code organisation only.

// PlaceholderFunc renders the placeholder text that replaces an elided
// tool_result Content. It receives the original tool_call_id and the
// byte length of the elided text.
//
// Deprecated: PlaceholderFunc cannot convey *why* a message was elided
// (keep_last_k vs stale_resource). New code should use PlaceholderV2Func
// via WithPlaceholderV2; the legacy form is retained for backwards
// compatibility with callers that wired their own placeholder template
// before the multi-strategy editor existed.
type PlaceholderFunc func(toolCallID string, originalBytes int) string

// DefaultContextEditPlaceholder is the legacy built-in placeholder
// template. Kept verbatim so callers that compare the wire form against
// a fixed string (e.g. golden tests) do not have to update.
func DefaultContextEditPlaceholder(toolCallID string, originalBytes int) string {
	return fmt.Sprintf("[context_edited: tool_result %s elided, %d bytes]", toolCallID, originalBytes)
}

// WithPlaceholder customises the placeholder text. The function
// receives the original tool_call_id and the byte length of the
// elided text content.
//
// Deprecated: prefer WithPlaceholderV2 — the V2 form receives the
// editor's reason and detail, allowing prompts to surface *why* a
// fold happened. WithPlaceholder is preserved so existing callers do
// not break, but it cannot express stale_resource or elide_to_artifact
// context. When both options are configured, V2 wins.
func WithPlaceholder(fn PlaceholderFunc) ContextEditorOption {
	return func(m *ContextEditorMiddleware) {
		if fn != nil {
			m.placeholderFn = fn
		}
	}
}

// renderPlaceholder selects the placeholder template. V2 (when wired)
// always wins because it can convey reason+detail; the legacy V1
// template is only used when the editor is operating in pure
// keep_last_k mode and the caller did not opt into V2 — that path is
// preserved verbatim so existing wire-format expectations hold.
func (m *ContextEditorMiddleware) renderPlaceholder(toolCallID string, originalBytes int, reason, detail string) string {
	if m.placeholderV2 != nil {
		return m.placeholderV2(toolCallID, originalBytes, reason, detail)
	}
	if m.usesV2Defaults() && reason != "" {
		return DefaultContextEditPlaceholderV2(toolCallID, originalBytes, reason, detail)
	}
	return m.placeholderFn(toolCallID, originalBytes)
}

// usesV2Defaults reports whether the editor has any new strategy wired
// in that the legacy V1 placeholder cannot express. When so, the V2
// default is used so the prompt actually surfaces the reason; when not,
// the V1 default is preserved to maintain bit-for-bit wire compat with
// callers from before stale_resource / elide_to_artifact existed.
func (m *ContextEditorMiddleware) usesV2Defaults() bool {
	return m.resourceLookup != nil || m.maxBytesPerMessage > 0
}
