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

package read

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

const (
	toolName        = "read"
	toolDescription = "Read the contents of a file or list directory entries. When the path is a directory, returns a listing of its entries. When the path is a file, returns the file content as text. Supports optional line offset and limit for reading portions of large files. Set show_line_numbers to true to include line numbers in the output."

	maxDirEntries = 200

	defaultMaxReadBytes = 256 * 1024  // 256KB
	maxScanLineBytes    = 1024 * 1024 // 1MB max single line (separate from total read limit)
)

// ReadTool holds configuration for the built-in file read tool.
type ReadTool struct {
	allowedDirs  []string
	guard        *toolkit.PathGuard
	maxReadBytes int
	readTracker  toolkit.ReadTracker
}

// Option is a functional option for configuring a ReadTool.
type Option func(*ReadTool)

// WithMaxReadBytes sets the maximum number of bytes to read.
func WithMaxReadBytes(n int) Option {
	return func(rt *ReadTool) { rt.maxReadBytes = n }
}

// WithReadTracker sets a ReadTracker that records successful file reads.
// When configured, the read handler will call RecordRead after each
// successful file read (not for directory listings).
func WithReadTracker(tracker toolkit.ReadTracker) Option {
	return func(rt *ReadTool) { rt.readTracker = tracker }
}

// WithAllowedDirs sets the allowed base directories for the read tool. When
// set in isolation it uses ValidatePath semantics for backward compatibility.
// For TOCTOU-safe enforcement via os.Root prefer WithPathGuard.
func WithAllowedDirs(dirs ...string) Option {
	return func(rt *ReadTool) {
		rt.allowedDirs = toolkit.CleanAllowedDirs(dirs)
	}
}

// WithPathGuard installs a PathGuard which enforces os.Root-bounded access.
// When installed, takes precedence over allowedDirs.
func WithPathGuard(g *toolkit.PathGuard) Option {
	return func(rt *ReadTool) { rt.guard = g }
}

// New creates a ReadTool with the given options.
func New(opts ...Option) *ReadTool {
	rt := &ReadTool{maxReadBytes: defaultMaxReadBytes}
	for _, o := range opts {
		o(rt)
	}

	return rt
}

// ToolDef returns the schema.ToolDef for registration.
func (rt *ReadTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        toolName,
		Description: toolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the file to read.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based line number to start reading from. Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return. Defaults to reading the entire file.",
				},
				"show_line_numbers": map[string]any{
					"type":        "boolean",
					"description": "When true, prefix each line with its line number and a tab character (e.g. '42\\tcontent'). Defaults to false.",
				},
			},
			"required":             []string{"file_path"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this read tool.
func (rt *ReadTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return schema.ErrorResult("", "read tool: "+err.Error()), nil
		}

		var parsed struct {
			FilePath        string `json:"file_path"`
			Offset          *int   `json:"offset"`
			Limit           *int   `json:"limit"`
			ShowLineNumbers bool   `json:"show_line_numbers"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "read tool: invalid arguments: "+err.Error()), nil
		}

		var (
			cleaned string
			info    os.FileInfo
			f       *os.File
			err     error
		)

		if rt.guard.Allowed() {
			var rel string
			var root *os.Root
			cleaned, rel, root, err = rt.guard.Check("read", parsed.FilePath)
			if err != nil {
				return schema.ErrorResult("", err.Error()), nil
			}

			info, err = root.Stat(rel)
			if err != nil {
				if os.IsNotExist(err) {
					return schema.ErrorResult("", fmt.Sprintf("read tool: file does not exist: %s", cleaned)), nil
				}
				return schema.ErrorResult("", "read tool: "+err.Error()), nil
			}

			f, err = root.Open(rel)
			if err != nil {
				return schema.ErrorResult("", "read tool: "+err.Error()), nil
			}
		} else {
			cleaned, err = toolkit.ValidatePath("read", parsed.FilePath, rt.allowedDirs)
			if err != nil {
				return schema.ErrorResult("", err.Error()), nil
			}

			info, err = os.Stat(cleaned)
			if err != nil {
				if os.IsNotExist(err) {
					return schema.ErrorResult("", fmt.Sprintf("read tool: file does not exist: %s", cleaned)), nil
				}
				return schema.ErrorResult("", "read tool: "+err.Error()), nil
			}

			if !info.IsDir() {
				f, err = os.Open(cleaned)
				if err != nil {
					return schema.ErrorResult("", "read tool: "+err.Error()), nil
				}
			}
		}

		offset := 1
		if parsed.Offset != nil {
			offset = *parsed.Offset
			if offset < 1 {
				if f != nil {
					_ = f.Close()
				}
				return schema.ErrorResult("", "read tool: offset must be >= 1"), nil
			}
		}

		limitSet := false
		limit := 0

		if parsed.Limit != nil {
			limit = *parsed.Limit
			if limit < 1 {
				if f != nil {
					_ = f.Close()
				}
				return schema.ErrorResult("", "read tool: limit must be >= 1"), nil
			}

			limitSet = true
		}

		if info.IsDir() {
			if f != nil {
				defer func() { _ = f.Close() }()
				return listDirectoryFile(f, cleaned)
			}
			return listDirectory(cleaned)
		}

		defer func() { _ = f.Close() }()

		scanner := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, maxScanLineBytes)

		var result []byte

		lineNum := 0
		linesRead := 0
		totalBytes := 0

		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return schema.ErrorResult("", "read tool: "+err.Error()), nil
			}

			lineNum++

			if lineNum < offset {
				continue
			}

			line := scanner.Bytes()
			lineLen := len(line) + 1 // +1 for newline

			if totalBytes+lineLen > rt.maxReadBytes {
				remaining := rt.maxReadBytes - totalBytes
				if remaining > 0 {
					if len(result) > 0 {
						result = append(result, '\n')
					}

					truncLine := line[:remaining]
					if parsed.ShowLineNumbers {
						result = fmt.Appendf(result, "%d\t%s", lineNum, truncLine)
					} else {
						result = append(result, truncLine...)
					}
				}

				result = fmt.Appendf(result, "\n... [output truncated, showing first %d bytes]", rt.maxReadBytes)

				if rt.readTracker != nil {
					rt.readTracker.RecordRead(cleaned)
				}

				return schema.TextResult("", string(result)), nil
			}

			if len(result) > 0 {
				result = append(result, '\n')
			}

			if parsed.ShowLineNumbers {
				result = fmt.Appendf(result, "%d\t%s", lineNum, line)
			} else {
				result = append(result, line...)
			}

			totalBytes += lineLen
			linesRead++

			if limitSet && linesRead >= limit {
				break
			}
		}

		if err := scanner.Err(); err != nil {
			return schema.ErrorResult("", "read tool: error reading file: "+err.Error()), nil
		}

		if rt.readTracker != nil {
			rt.readTracker.RecordRead(cleaned)
		}

		return schema.TextResult("", string(result)), nil
	}
}

// listDirectory returns a listing of directory entries (fallback path).
func listDirectory(dir string) (schema.ToolResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return schema.ErrorResult("", "read tool: "+err.Error()), nil
	}

	return formatDirectoryListing(entries, dir), nil
}

// listDirectoryFile returns a listing via an already-open *os.File (guard path).
func listDirectoryFile(f *os.File, displayDir string) (schema.ToolResult, error) {
	entries, err := f.ReadDir(-1)
	if err != nil {
		return schema.ErrorResult("", "read tool: "+err.Error()), nil
	}

	return formatDirectoryListing(entries, displayDir), nil
}

func formatDirectoryListing(entries []os.DirEntry, displayDir string) schema.ToolResult {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Directory: %s\n\n", displayDir)

	count := 0

	for _, e := range entries {
		if count >= maxDirEntries {
			fmt.Fprintf(&sb, "\n... truncated, showing first %d of %d entries", maxDirEntries, len(entries))

			break
		}

		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}

		fmt.Fprintf(&sb, "%s%s\n", e.Name(), suffix)
		count++
	}

	return schema.TextResult("", sb.String())
}

// Register creates a ReadTool and registers it in the given registry.
func Register(registry *tool.Registry, opts ...Option) error {
	rt := New(opts...)
	return registry.RegisterIfAbsent(rt.ToolDef(), rt.Handler())
}
