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

package grep

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

const (
	defaultGrepTimeout   = 30 * time.Second
	defaultGrepMaxOutput = 256 * 1024 // 256KB
	grepToolName         = "grep"
	grepToolDescription  = "Search file contents by regular expression pattern. Returns matching lines with file paths and line numbers."
)

// GrepTool holds configuration for the built-in grep content search tool.
type GrepTool struct {
	timeout        time.Duration
	workingDir     string
	allowedDirs    []string
	guard          *toolkit.PathGuard
	maxOutputBytes int
}

// Option is a functional option for configuring a GrepTool.
type Option func(*GrepTool)

// WithTimeout sets the command execution timeout.
func WithTimeout(d time.Duration) Option {
	return func(gt *GrepTool) { gt.timeout = d }
}

// WithWorkingDir sets the default search path when no path argument is provided.
func WithWorkingDir(dir string) Option {
	return func(gt *GrepTool) { gt.workingDir = dir }
}

// WithAllowedDirs sets the allowed base directories for the grep tool.
func WithAllowedDirs(dirs ...string) Option {
	return func(gt *GrepTool) {
		gt.allowedDirs = toolkit.CleanAllowedDirs(dirs)
	}
}

// WithPathGuard installs a PathGuard used to validate the search path.
func WithPathGuard(g *toolkit.PathGuard) Option {
	return func(gt *GrepTool) { gt.guard = g }
}

// WithMaxOutput sets the maximum output size in bytes.
func WithMaxOutput(n int) Option {
	return func(gt *GrepTool) { gt.maxOutputBytes = n }
}

// New creates a GrepTool with the given options.
// Defaults: timeout=30s, maxOutputBytes=256KB.
func New(opts ...Option) *GrepTool {
	gt := &GrepTool{
		timeout:        defaultGrepTimeout,
		maxOutputBytes: defaultGrepMaxOutput,
	}
	for _, o := range opts {
		o(gt)
	}

	return gt
}

// ToolDef returns the schema.ToolDef for registration.
func (gt *GrepTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        grepToolName,
		Description: grepToolDescription,
		Source:      schema.ToolSourceLocal,
		ReadOnly:    true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression pattern to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to a file or directory to search in. Defaults to the configured working directory.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Glob pattern to filter files (e.g. '*.go', '*.{ts,tsx}').",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this grep tool.
func (gt *GrepTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return schema.ErrorResult("", "grep tool: "+err.Error()), nil
		}

		var parsed struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
			Include string `json:"include"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "grep tool: invalid arguments: "+err.Error()), nil
		}

		if parsed.Pattern == "" {
			return schema.ErrorResult("", "grep tool: pattern must not be empty"), nil
		}

		// Resolve search path.
		searchPath := parsed.Path
		if searchPath == "" {
			searchPath = gt.workingDir
		}

		if searchPath == "" {
			return schema.ErrorResult("", "grep tool: no search path provided and no working directory configured"), nil
		}

		// Normalize path to resolve ".." components.
		searchPath = filepath.Clean(searchPath)

		// Validate path via guard (if installed) or allowedDirs.
		switch {
		case gt.guard.Allowed():
			if _, _, _, err := gt.guard.Check("grep", searchPath); err != nil {
				return schema.ErrorResult("", err.Error()), nil
			}
		case len(gt.allowedDirs) > 0:
			if _, err := toolkit.ValidatePath("grep", searchPath, gt.allowedDirs); err != nil {
				return schema.ErrorResult("", err.Error()), nil
			}
		}

		// Build and execute the grep command.
		result, err := gt.execute(ctx, searchPath, parsed.Pattern, parsed.Include)
		if err != nil {
			return schema.ErrorResult("", "grep tool: "+err.Error()), nil
		}

		// Handle exit codes per grep/rg semantics.
		switch result.ExitCode {
		case 0:
			output := result.Stdout
			if result.Truncated {
				output += fmt.Sprintf("\n... [output truncated, showing first %d bytes]", gt.maxOutputBytes)
			}

			return schema.TextResult("", output), nil
		case 1:
			// Exit code 1: no matches found (normal for grep/rg).
			output := result.Stdout
			if output == "" {
				output = "No matches found."
			}

			return schema.TextResult("", output), nil
		default:
			// Exit code >= 2: real error. Include stderr for diagnostics.
			errMsg := result.Stdout
			if result.Stderr != "" {
				errMsg = strings.TrimSpace(errMsg + "\n" + result.Stderr)
			}

			return schema.ErrorResult("", errMsg), nil
		}
	}
}

// execute runs the grep command and returns the result.
func (gt *GrepTool) execute(parentCtx context.Context, searchPath, pattern, include string) (toolkit.RunResult, error) {
	childCtx, cancel := context.WithTimeout(parentCtx, gt.timeout)
	defer cancel()

	cmd, err := buildGrepCommand(childCtx, searchPath, pattern, include)
	if err != nil {
		return toolkit.RunResult{}, err
	}

	setProcAttr(cmd)
	setCancelFunc(cmd)

	return toolkit.RunCommand(cmd, cancel, parentCtx, childCtx, gt.maxOutputBytes, gt.timeout)
}

// Register creates a GrepTool and registers it in the given registry.
// Returns an error if a tool named "grep" is already registered.
func Register(registry *tool.Registry, opts ...Option) error {
	gt := New(opts...)
	return registry.RegisterIfAbsent(gt.ToolDef(), gt.Handler())
}
