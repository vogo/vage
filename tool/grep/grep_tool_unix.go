//go:build !windows

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
	"fmt"
	"os/exec"
	"sync"
	"syscall"
)

var (
	grepCmdPath string
	grepCmdType string // "rg" or "grep"
	grepCmdOnce sync.Once
	grepCmdErr  error
)

func resolveGrepCommand() (string, string, error) {
	grepCmdOnce.Do(func() {
		if p, err := exec.LookPath("rg"); err == nil {
			grepCmdPath = p
			grepCmdType = "rg"

			return
		}

		if p, err := exec.LookPath("grep"); err == nil {
			grepCmdPath = p
			grepCmdType = "grep"

			return
		}

		grepCmdErr = fmt.Errorf("grep tool: neither 'rg' (ripgrep) nor 'grep' found in PATH; please install one")
	})

	return grepCmdPath, grepCmdType, grepCmdErr
}

func buildGrepCommand(ctx context.Context, searchPath, pattern, include string) (*exec.Cmd, error) {
	cmdPath, cmdType, err := resolveGrepCommand()
	if err != nil {
		return nil, err
	}

	switch cmdType {
	case "rg":
		args := []string{"--line-number", "--no-heading", pattern}
		if include != "" {
			args = append(args, "--glob", include)
		}

		args = append(args, searchPath)

		return exec.CommandContext(ctx, cmdPath, args...), nil
	default: // "grep"
		args := []string{"-rn", pattern}
		if include != "" {
			args = append(args, "--include="+include)
		}

		args = append(args, searchPath)

		return exec.CommandContext(ctx, cmdPath, args...), nil
	}
}

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func setCancelFunc(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
