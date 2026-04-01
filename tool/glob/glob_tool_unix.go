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

package glob

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

var (
	findCmdPath   string
	findHasPrintf bool // true if find supports -printf (GNU find)
	findCmdOnce   sync.Once
	findCmdErr    error
)

func resolveFindCommand() (string, bool, error) {
	findCmdOnce.Do(func() {
		findCmdPath, findCmdErr = exec.LookPath("find")
		if findCmdErr != nil {
			findCmdErr = fmt.Errorf("glob tool: 'find' command not found; please install it")

			return
		}

		// Probe whether find supports -printf (GNU find does, BSD find does not).
		out, err := exec.Command(findCmdPath, "/dev/null", "-maxdepth", "0", "-printf", "ok").Output()
		findHasPrintf = err == nil && string(out) == "ok"
	})

	return findCmdPath, findHasPrintf, findCmdErr
}

func buildGlobCommand(ctx context.Context, dir, pattern string) (*exec.Cmd, error) {
	cmdPath, hasPrintf, err := resolveFindCommand()
	if err != nil {
		return nil, err
	}

	fullPattern := filepath.Join(dir, pattern)

	if hasPrintf {
		// GNU find: -printf outputs "mtime_epoch\tpath\n" so Go can sort
		// by mtime without extra stat calls.
		cmd := exec.CommandContext(ctx, cmdPath, dir, "-path", fullPattern, "-type", "f",
			"-printf", "%T@\t%p\n")

		return cmd, nil
	}

	// BSD find (macOS): no -printf, output plain paths.
	cmd := exec.CommandContext(ctx, cmdPath, dir, "-path", fullPattern, "-type", "f")

	return cmd, nil
}

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func setCancelFunc(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
