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

package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

const (
	toolName        = "edit"
	toolDescription = "Edit a file by performing exact string replacements. Finds old_string in the file and replaces it with new_string. By default, requires a unique match; set replace_all to true to replace all occurrences."

	defaultMaxEditFileBytes int64 = 10 * 1024 * 1024 // 10 MB
	snippetContextLines           = 3
)

// EditTool holds configuration for the built-in file edit tool.
type EditTool struct {
	allowedDirs  []string
	maxFileBytes int64
	denyRules    []string
	readTracker  toolkit.ReadTracker
}

// Option is a functional option for configuring an EditTool.
type Option func(*EditTool)

// WithMaxFileBytes sets the maximum file size in bytes that can be edited.
func WithMaxFileBytes(n int64) Option {
	return func(et *EditTool) { et.maxFileBytes = n }
}

// WithDenyRules sets glob patterns for file basenames that are not allowed
// to be edited (e.g., "*.env", "*.lock", "credentials.json"). Patterns are
// matched against filepath.Base(path) using filepath.Match.
func WithDenyRules(patterns ...string) Option {
	return func(et *EditTool) { et.denyRules = patterns }
}

// WithReadTracker sets a ReadTracker that requires files to be read before
// editing. When configured, the edit handler will reject edits to files that
// have not been recorded via the tracker.
func WithReadTracker(tracker toolkit.ReadTracker) Option {
	return func(et *EditTool) { et.readTracker = tracker }
}

// WithAllowedDirs sets the allowed base directories for the edit tool.
func WithAllowedDirs(dirs ...string) Option {
	return func(et *EditTool) {
		et.allowedDirs = toolkit.CleanAllowedDirs(dirs)
	}
}

// New creates an EditTool with the given options.
func New(opts ...Option) *EditTool {
	et := &EditTool{maxFileBytes: defaultMaxEditFileBytes}
	for _, o := range opts {
		o(et)
	}

	return et
}

// ToolDef returns the schema.ToolDef for registration.
func (et *EditTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        toolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the file to edit.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact text to find in the file. Must not be empty.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement text. Must differ from old_string. Can be empty to delete the matched text.",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "When true, replace all occurrences. When false (default), replace only the first occurrence but error if multiple matches exist.",
				},
			},
			"required":             []string{"file_path", "old_string", "new_string"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this edit tool.
func (et *EditTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return schema.ErrorResult("", "edit tool: "+err.Error()), nil
		}

		var parsed struct {
			FilePath   string `json:"file_path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "edit tool: invalid arguments: "+err.Error()), nil
		}

		cleaned, err := toolkit.ValidatePath("edit", parsed.FilePath, et.allowedDirs)
		if err != nil {
			return schema.ErrorResult("", err.Error()), nil
		}

		if matched := matchDenyRule(cleaned, et.denyRules); matched != "" {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: file is protected by deny rule %q: %s", matched, cleaned)), nil
		}

		if et.readTracker != nil && !et.readTracker.HasRead(cleaned) {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: file must be read before editing; use the read tool first: %s", cleaned)), nil
		}

		if parsed.OldString == "" {
			return schema.ErrorResult("", "edit tool: old_string must not be empty"), nil
		}

		if parsed.OldString == parsed.NewString {
			return schema.ErrorResult("", "edit tool: old_string and new_string must differ"), nil
		}

		// Acquire a process-level lock for this file to prevent concurrent
		// read-modify-write races (TOCTOU).
		unlock := toolkit.LockPath(cleaned)
		defer unlock()

		info, err := os.Stat(cleaned)
		if err != nil {
			if os.IsNotExist(err) {
				return schema.ErrorResult("", fmt.Sprintf("edit tool: file does not exist: %s", cleaned)), nil
			}

			return schema.ErrorResult("", "edit tool: "+err.Error()), nil
		}

		if info.IsDir() {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: path is a directory, not a file: %s", cleaned)), nil
		}

		if info.Mode().Perm()&0o200 == 0 {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: file appears to be read-only (mode %s): %s", info.Mode().Perm(), cleaned)), nil
		}

		if info.Size() > et.maxFileBytes {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: file size (%d bytes) exceeds maximum allowed (%d bytes): %s", info.Size(), et.maxFileBytes, cleaned)), nil
		}

		content, err := os.ReadFile(cleaned)
		if err != nil {
			return schema.ErrorResult("", "edit tool: "+err.Error()), nil
		}

		contentStr := string(content)
		count := strings.Count(contentStr, parsed.OldString)

		if count == 0 {
			return schema.ErrorResult("", fmt.Sprintf("edit tool: old_string not found in file. Possible causes: whitespace/indentation mismatch, or the file may have changed since last read. File: %s", cleaned)), nil
		}

		if count > 1 && !parsed.ReplaceAll {
			return schema.ErrorResult("", fmt.Sprintf(
				"edit tool: old_string matches %d locations; provide more context to make the match unique, or set replace_all to true",
				count,
			)), nil
		}

		// Record position of first change for the snippet.
		changePos := strings.Index(contentStr, parsed.OldString)

		var newContent string
		if parsed.ReplaceAll {
			newContent = strings.ReplaceAll(contentStr, parsed.OldString, parsed.NewString)
		} else {
			newContent = strings.Replace(contentStr, parsed.OldString, parsed.NewString, 1)
		}

		// Atomic write-back: write to temp file then rename.
		if writeErr := toolkit.AtomicWriteFile(cleaned, []byte(newContent), info.Mode().Perm()); writeErr != nil {
			return schema.ErrorResult("", "edit tool: "+writeErr.Error()), nil
		}

		msg := fmt.Sprintf("replaced %d occurrence(s) in %s", count, cleaned)

		if snippet := toolkit.GenerateEditSnippet(newContent, changePos, snippetContextLines); snippet != "" {
			msg += "\n--- snippet ---\n" + snippet
		}

		return schema.TextResult("", msg), nil
	}
}

// matchDenyRule checks path against a list of glob patterns. Patterns are
// matched against filepath.Base(path) using filepath.Match. Returns the first
// matching pattern, or "" if none match.
func matchDenyRule(path string, patterns []string) string {
	base := filepath.Base(path)

	for _, p := range patterns {
		matched, err := filepath.Match(p, base)
		if err != nil {
			// Invalid pattern; skip gracefully.
			continue
		}

		if matched {
			return p
		}
	}

	return ""
}

// Register creates an EditTool and registers it in the given registry.
func Register(registry *tool.Registry, opts ...Option) error {
	et := New(opts...)
	return registry.RegisterIfAbsent(et.ToolDef(), et.Handler())
}
