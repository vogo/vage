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

package glob

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

const (
	defaultGlobTimeout   = 30 * time.Second
	defaultGlobMaxOutput = 256 * 1024 // 256KB
	defaultMaxResults    = 1000
	globToolName         = "glob"
	globToolDescription  = "Search for files by glob pattern within a directory tree. Returns matching file paths sorted by modification time (most recent first), one path per line."
)

// GlobTool holds configuration for the built-in glob file search tool.
type GlobTool struct {
	timeout        time.Duration
	workingDir     string
	allowedDirs    []string
	maxOutputBytes int
	maxResults     int
}

// Option is a functional option for configuring a GlobTool.
type Option func(*GlobTool)

// WithTimeout sets the command execution timeout.
func WithTimeout(d time.Duration) Option {
	return func(gt *GlobTool) { gt.timeout = d }
}

// WithWorkingDir sets the default search directory when no path argument is provided.
func WithWorkingDir(dir string) Option {
	return func(gt *GlobTool) { gt.workingDir = dir }
}

// WithAllowedDirs sets the allowed base directories for the glob tool.
func WithAllowedDirs(dirs ...string) Option {
	return func(gt *GlobTool) {
		gt.allowedDirs = toolkit.CleanAllowedDirs(dirs)
	}
}

// WithMaxOutput sets the maximum output size in bytes.
func WithMaxOutput(n int) Option {
	return func(gt *GlobTool) { gt.maxOutputBytes = n }
}

// WithMaxResults sets the maximum number of file paths to return.
func WithMaxResults(n int) Option {
	return func(gt *GlobTool) { gt.maxResults = n }
}

// New creates a GlobTool with the given options.
// Defaults: timeout=30s, maxOutputBytes=256KB, maxResults=1000.
func New(opts ...Option) *GlobTool {
	gt := &GlobTool{
		timeout:        defaultGlobTimeout,
		maxOutputBytes: defaultGlobMaxOutput,
		maxResults:     defaultMaxResults,
	}
	for _, o := range opts {
		o(gt)
	}

	return gt
}

// ToolDef returns the schema.ToolDef for registration.
func (gt *GlobTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        globToolName,
		Description: globToolDescription,
		Source:      schema.ToolSourceLocal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match files, relative to the search directory (e.g. '**/*.go', 'src/**/*.ts'). Must not contain '..' components.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the directory to search in. Defaults to the configured working directory.",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this glob tool.
func (gt *GlobTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return schema.ErrorResult("", "glob tool: "+err.Error()), nil
		}

		var parsed struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "glob tool: invalid arguments: "+err.Error()), nil
		}

		if parsed.Pattern == "" {
			return schema.ErrorResult("", "glob tool: pattern must not be empty"), nil
		}

		if strings.HasPrefix(parsed.Pattern, "/") {
			return schema.ErrorResult("", "glob tool: pattern must not be absolute"), nil
		}

		if containsDotDot(parsed.Pattern) {
			return schema.ErrorResult("", "glob tool: pattern must not contain '..' components"), nil
		}

		// Resolve search directory.
		dir := parsed.Path
		if dir == "" {
			dir = gt.workingDir
		}

		if dir == "" {
			return schema.ErrorResult("", "glob tool: no search directory provided and no working directory configured"), nil
		}

		// Normalize path to resolve ".." components.
		dir = filepath.Clean(dir)

		// Validate path if allowedDirs is set.
		if len(gt.allowedDirs) > 0 {
			if _, err := toolkit.ValidatePath("glob", dir, gt.allowedDirs); err != nil {
				return schema.ErrorResult("", err.Error()), nil
			}
		}

		// Verify the directory exists and is a directory.
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return schema.ErrorResult("", fmt.Sprintf("glob tool: directory does not exist: %s", dir)), nil
			}

			return schema.ErrorResult("", "glob tool: "+err.Error()), nil
		}

		if !info.IsDir() {
			return schema.ErrorResult("", fmt.Sprintf("glob tool: path is not a directory: %s", dir)), nil
		}

		// Build and execute the glob command.
		result, err := gt.execute(ctx, dir, parsed.Pattern)
		if err != nil {
			return schema.ErrorResult("", "glob tool: "+err.Error()), nil
		}

		// Parse output into file paths and sort by modification time.
		sorted := parseAndSortPaths(result.Stdout)

		// Truncate to maxResults.
		resultsLimited := len(sorted) > gt.maxResults
		if resultsLimited {
			sorted = sorted[:gt.maxResults]
		}

		text := strings.Join(sorted, "\n")

		// Append truncation notices so the caller knows results are incomplete.
		if result.Truncated {
			text += fmt.Sprintf("\n... [output truncated at %d bytes, results may be incomplete]", gt.maxOutputBytes)
		}

		if resultsLimited {
			text += fmt.Sprintf("\n... [results limited to %d entries]", gt.maxResults)
		}

		return schema.TextResult("", text), nil
	}
}

// execute runs the glob command and returns the result.
// In git repositories, it prefers "git ls-files" which automatically respects
// .gitignore rules. Falls back to platform-specific find/PowerShell otherwise.
func (gt *GlobTool) execute(parentCtx context.Context, dir, pattern string) (toolkit.RunResult, error) {
	childCtx, cancel := context.WithTimeout(parentCtx, gt.timeout)
	defer cancel()

	// Try git ls-files first if inside a git repo.
	if isGitRepo(dir) {
		if cmd := buildGitCommand(childCtx, dir, pattern); cmd != nil {
			cmd.Dir = dir

			setProcAttr(cmd)
			setCancelFunc(cmd)

			result, err := toolkit.RunCommand(cmd, cancel, parentCtx, childCtx, gt.maxOutputBytes, gt.timeout)
			if err == nil && result.ExitCode == 0 {
				// git ls-files returns relative paths; convert to absolute.
				result.Stdout = gt.toAbsolutePaths(dir, result.Stdout)

				return result, nil
			}

			// git ls-files failed — fall through to find/PowerShell.
		}
	}

	cmd, err := buildGlobCommand(childCtx, dir, pattern)
	if err != nil {
		return toolkit.RunResult{}, err
	}

	cmd.Dir = dir

	setProcAttr(cmd)
	setCancelFunc(cmd)

	result, err := toolkit.RunCommand(cmd, cancel, parentCtx, childCtx, gt.maxOutputBytes, gt.timeout)
	if err != nil {
		return toolkit.RunResult{}, err
	}

	// find returns exit code 1 for some non-fatal issues (e.g., permission denied
	// on a subdirectory). If we got partial output, treat it as success.
	if result.ExitCode == 1 && result.Stdout != "" {
		result.ExitCode = 0
	} else if result.ExitCode != 0 && result.Stdout == "" {
		return toolkit.RunResult{}, fmt.Errorf("exit code: %d", result.ExitCode)
	}

	return result, nil
}

// toAbsolutePaths converts newline-separated relative paths to absolute paths.
func (gt *GlobTool) toAbsolutePaths(dir, output string) string {
	lines := strings.Split(output, "\n")

	var result []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if filepath.IsAbs(line) {
			result = append(result, line)
		} else {
			result = append(result, filepath.Join(dir, line))
		}
	}

	return strings.Join(result, "\n")
}

// containsDotDot checks if a pattern contains ".." path components.
func containsDotDot(pattern string) bool {
	// Normalize backslashes to forward slashes so this check
	// works on Windows paths as well.
	normalized := strings.ReplaceAll(pattern, "\\", "/")
	return slices.Contains(strings.Split(normalized, "/"), "..")
}

// fileEntry holds a file path and its modification time for sorting.
type fileEntry struct {
	path    string
	modTime float64 // epoch seconds from find -printf or os.Stat
}

// parseAndSortPaths parses find output lines (which may be "mtime\tpath" or plain paths),
// sorts by modification time (most recent first), and returns the sorted paths.
func parseAndSortPaths(output string) []string {
	lines := strings.Split(output, "\n")
	entries := make([]fileEntry, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try to parse "epoch_seconds\tpath" format from find -printf.
		if idx := strings.IndexByte(line, '\t'); idx > 0 {
			if mtime, err := strconv.ParseFloat(line[:idx], 64); err == nil {
				entries = append(entries, fileEntry{path: line[idx+1:], modTime: mtime})

				continue
			}
		}

		// Fallback: plain path, stat for mtime (Windows / -printf not available).
		info, err := os.Stat(line)
		if err != nil {
			continue // skip files that vanished between find and stat
		}

		entries = append(entries, fileEntry{
			path:    line,
			modTime: float64(info.ModTime().UnixNano()) / 1e9,
		})
	}

	slices.SortFunc(entries, func(a, b fileEntry) int {
		return cmp.Compare(b.modTime, a.modTime)
	})

	result := make([]string, len(entries))
	for i, e := range entries {
		result[i] = e.path
	}

	return result
}

// Register creates a GlobTool and registers it in the given registry.
// Returns an error if a tool named "glob" is already registered.
func Register(registry *tool.Registry, opts ...Option) error {
	gt := New(opts...)
	return registry.RegisterIfAbsent(gt.ToolDef(), gt.Handler())
}
