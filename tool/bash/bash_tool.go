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

package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
	"unicode/utf8"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const (
	defaultBashTimeout   = 30 * time.Second
	defaultBashMaxOutput = 256 * 1024 // 256KB
	bashToolName         = "bash"
	bashToolDescription  = "Execute a bash shell command on the host system. Returns the command output (combined stdout and stderr). Non-zero exit codes are reported as errors with the exit code included."
)

// BashTool holds configuration for the built-in bash command executor.
type BashTool struct {
	timeout        time.Duration
	workingDir     string
	maxOutputBytes int
	pathGuardian   *PathGuardian
}

// Option is a functional option for configuring a BashTool.
type Option func(*BashTool)

// WithTimeout sets the command execution timeout.
func WithTimeout(d time.Duration) Option {
	return func(bt *BashTool) { bt.timeout = d }
}

// WithWorkingDir sets the working directory for command execution.
func WithWorkingDir(dir string) Option {
	return func(bt *BashTool) { bt.workingDir = dir }
}

// WithMaxOutput sets the maximum output size in bytes.
func WithMaxOutput(n int) Option {
	return func(bt *BashTool) { bt.maxOutputBytes = n }
}

// WithPathGuardian installs a path guardian that hard-blocks commands
// classified as TierBlocked before execution.
func WithPathGuardian(g *PathGuardian) Option {
	return func(bt *BashTool) { bt.pathGuardian = g }
}

// New creates a BashTool with the given options.
// Defaults: timeout=30s, maxOutputBytes=256KB.
func New(opts ...Option) *BashTool {
	bt := &BashTool{
		timeout:        defaultBashTimeout,
		maxOutputBytes: defaultBashMaxOutput,
	}
	for _, o := range opts {
		o(bt)
	}

	return bt
}

// ToolDef returns the schema.ToolDef for registration.
func (bt *BashTool) ToolDef() schema.ToolDef {
	return schema.ToolDef{
		Name:        bashToolName,
		Description: bashToolDescription,
		Source:      schema.ToolSourceLocal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute, e.g. 'ls -la' or 'date +%Y-%m-%d'",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

// Handler returns the ToolHandler closure for this bash tool.
func (bt *BashTool) Handler() tool.ToolHandler {
	return func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		var parsed struct {
			Command string `json:"command"`
		}

		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return schema.ErrorResult("", "bash tool: invalid arguments: "+err.Error()), nil
		}

		if parsed.Command == "" {
			return schema.ErrorResult("", "bash tool: command must not be empty"), nil
		}

		return bt.execute(ctx, parsed.Command)
	}
}

// execute runs the command and returns the tool result.
func (bt *BashTool) execute(parentCtx context.Context, command string) (schema.ToolResult, error) {
	if bt.pathGuardian != nil {
		if cls := bt.pathGuardian.Classify(command); cls.Tier == TierBlocked {
			return schema.ErrorResult("", fmt.Sprintf("bash tool: blocked by rule %q: %s", cls.Rule, cls.Reason)), nil
		}
	}

	childCtx, cancel := context.WithTimeout(parentCtx, bt.timeout)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "/bin/sh", "-c", command)

	if bt.workingDir != "" {
		cmd.Dir = bt.workingDir
	}

	setProcAttr(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return schema.ErrorResult("", "bash tool: "+err.Error()), nil
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return schema.ErrorResult("", "bash tool: "+err.Error()), nil
	}

	setCancelFunc(cmd)

	if err := cmd.Start(); err != nil {
		return schema.ErrorResult("", "bash tool: "+err.Error()), nil
	}

	combined := io.MultiReader(stdoutPipe, stderrPipe)
	limitReader := &io.LimitedReader{R: combined, N: int64(bt.maxOutputBytes) + 1}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, limitReader)

	truncated := limitReader.N <= 0

	waitErr := cmd.Wait()

	output := buf.String()

	if truncated {
		// Truncate at a valid UTF-8 boundary to avoid splitting multi-byte runes.
		truncBytes := output[:bt.maxOutputBytes]
		for len(truncBytes) > 0 && !utf8.Valid([]byte(truncBytes)) {
			truncBytes = truncBytes[:len(truncBytes)-1]
		}

		output = truncBytes + fmt.Sprintf("\n... [output truncated, showing first %d bytes]", bt.maxOutputBytes)
	}

	if waitErr != nil {
		if parentCtx.Err() == context.Canceled {
			return schema.ErrorResult("", output+"\ncommand cancelled"), nil
		}

		if childCtx.Err() == context.DeadlineExceeded {
			return schema.ErrorResult("", output+fmt.Sprintf("\ncommand timed out after %s", bt.timeout)), nil
		}

		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return schema.ErrorResult("", output+fmt.Sprintf("\nexit code: %d", exitErr.ExitCode())), nil
		}

		return schema.ErrorResult("", output+"\n"+waitErr.Error()), nil
	}

	return schema.TextResult("", output), nil
}

// Register creates a BashTool and registers it in the given registry.
// Returns an error if a tool named "bash" is already registered.
func Register(registry *tool.Registry, opts ...Option) error {
	bt := New(opts...)
	return registry.RegisterIfAbsent(bt.ToolDef(), bt.Handler())
}
