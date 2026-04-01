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

package write

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

const (
	toolName        = "write"
	toolDescription = "Write content to a file on the filesystem. Creates the file if it does not exist, or overwrites it if it does. Parent directories are created automatically. Set create_only to true to prevent overwriting existing files."

	defaultMaxWriteBytes = 1024 * 1024 // 1MB
)

// WriteTool holds configuration for the built-in file write tool.
type WriteTool struct {
	allowedDirs   []string
	maxWriteBytes int
}

// Option is a functional option for configuring a WriteTool.
type Option func(*WriteTool)

// WithMaxWriteBytes sets the maximum number of bytes that can be written.
func WithMaxWriteBytes(n int) Option {
	return func(wt *WriteTool) { wt.maxWriteBytes = n }
}

// WithAllowedDirs sets the allowed base directories for the write tool.
func WithAllowedDirs(dirs ...string) Option {
	return func(wt *WriteTool) {
		wt.allowedDirs = toolkit.CleanAllowedDirs(dirs)
	}
}

// New creates a WriteTool with the given options.
func New(opts ...Option) *WriteTool {
	wt := &WriteTool{maxWriteBytes: defaultMaxWriteBytes}
	for _, o := range opts {
		o(wt)
	}

	return wt
}

// ToolDef returns the schema.ToolDef for registration.
func (wt *WriteTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        toolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the file to create or overwrite.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full content to write to the file.",
				},
				"create_only": map[string]any{
					"type":        "boolean",
					"description": "When true, fail if the file already exists instead of overwriting. Defaults to false.",
				},
			},
			"required":             []string{"file_path", "content"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this write tool.
func (wt *WriteTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return schema.ErrorResult("", "write tool: "+err.Error()), nil
		}

		var parsed struct {
			FilePath   string `json:"file_path"`
			Content    string `json:"content"`
			CreateOnly bool   `json:"create_only"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "write tool: invalid arguments: "+err.Error()), nil
		}

		cleaned, err := toolkit.ValidatePath("write", parsed.FilePath, wt.allowedDirs)
		if err != nil {
			return schema.ErrorResult("", err.Error()), nil
		}

		if len(parsed.Content) > wt.maxWriteBytes {
			return schema.ErrorResult("", fmt.Sprintf("write tool: content exceeds maximum size (%d bytes)", wt.maxWriteBytes)), nil
		}

		// Determine file permissions: preserve existing or use default.
		var perm os.FileMode = toolkit.DefaultFilePermission

		if info, statErr := os.Stat(cleaned); statErr == nil {
			if parsed.CreateOnly {
				return schema.ErrorResult("", fmt.Sprintf("write tool: file already exists (create_only=true): %s", cleaned)), nil
			}

			perm = info.Mode().Perm()
		}

		if writeErr := toolkit.AtomicWriteFile(cleaned, []byte(parsed.Content), perm); writeErr != nil {
			return schema.ErrorResult("", "write tool: "+writeErr.Error()), nil
		}

		return schema.TextResult("", fmt.Sprintf("wrote %d bytes to %s", len(parsed.Content), cleaned)), nil
	}
}

// Register creates a WriteTool and registers it in the given registry.
func Register(registry *tool.Registry, opts ...Option) error {
	wt := New(opts...)
	return registry.RegisterIfAbsent(wt.ToolDef(), wt.Handler())
}
