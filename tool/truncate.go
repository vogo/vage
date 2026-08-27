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

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// TruncateUTF8 returns the longest prefix of s that is at most maxBytes bytes
// long and remains valid UTF-8, never splitting a multi-byte rune. When s
// already fits, it is returned unchanged; a maxBytes of zero yields the empty
// string. Negative limits are a caller precondition violation and carry no
// promise.
//
// maxBytes counts BYTES, not characters, runes or tokens. This is the entry
// point for byte-bounded sinks (logs, displays, command output). Context
// budgets are a different concern and must keep using token-level governance
// such as TruncatingToolRegistry; the two are not interchangeable.
//
// The result carries no ellipsis or truncation marker: attaching one is the
// caller's decision, since a marker is only meaningful where it is displayed.
func TruncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}

	t := s[:maxBytes]
	for len(t) > 0 && !utf8.Valid([]byte(t)) {
		t = t[:len(t)-1]
	}

	return t
}

// TruncatingToolRegistry wraps a ToolRegistry and truncates tool results
// that exceed maxTokens.
//
// TruncatingToolRegistry is safe for concurrent use if the underlying
// ToolRegistry is safe for concurrent use.
type TruncatingToolRegistry struct {
	inner     ToolRegistry
	maxTokens int
}

// Compile-time check: TruncatingToolRegistry implements ToolRegistry.
var _ ToolRegistry = (*TruncatingToolRegistry)(nil)

// NewTruncatingToolRegistry creates a truncating wrapper.
// If maxTokens <= 0, no truncation is applied.
func NewTruncatingToolRegistry(inner ToolRegistry, maxTokens int) *TruncatingToolRegistry {
	return &TruncatingToolRegistry{
		inner:     inner,
		maxTokens: maxTokens,
	}
}

// Register delegates to the inner registry.
func (t *TruncatingToolRegistry) Register(def schema.ToolDef, handler ToolHandler) error {
	return t.inner.Register(def, handler)
}

// Unregister delegates to the inner registry.
func (t *TruncatingToolRegistry) Unregister(name string) error {
	return t.inner.Unregister(name)
}

// Get delegates to the inner registry.
func (t *TruncatingToolRegistry) Get(name string) (schema.ToolDef, bool) {
	return t.inner.Get(name)
}

// List delegates to the inner registry.
func (t *TruncatingToolRegistry) List() []schema.ToolDef {
	return t.inner.List()
}

// Merge delegates to the inner registry.
func (t *TruncatingToolRegistry) Merge(defs []schema.ToolDef) {
	t.inner.Merge(defs)
}

// Execute delegates to the inner registry, then truncates text content parts
// exceeding maxTokens.
func (t *TruncatingToolRegistry) Execute(ctx context.Context, name, args string) (schema.ToolResult, error) {
	result, err := t.inner.Execute(ctx, name, args)
	if err != nil {
		return result, err
	}

	if t.maxTokens <= 0 || result.IsError {
		return result, nil
	}

	for i, part := range result.Content {
		if part.Type != "text" || part.Text == "" {
			continue
		}

		estimated := memory.EstimateTextTokens(part.Text)
		if estimated <= t.maxTokens {
			continue
		}

		// Keep roughly maxTokens worth of text (the estimator's 4 bytes per
		// token), delegating the rune-boundary cut to TruncateUTF8 so both
		// entry points share one algorithm. The marker is appended here and
		// not in TruncateUTF8: it is governance feedback, not a string helper
		// concern.
		truncated := TruncateUTF8(part.Text, t.maxTokens*4)
		marker := fmt.Sprintf("\n[truncated: showing first %d of %d estimated tokens]", t.maxTokens, estimated)
		result.Content[i].Text = truncated + marker
	}

	return result, nil
}
