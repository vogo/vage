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

package toolkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// RunResult holds the output from a command execution.
type RunResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

// RunCommand starts cmd, reads stdout (up to maxOutputBytes) and stderr,
// then waits for the command to finish. If stdout exceeds maxOutputBytes,
// it is truncated at a UTF-8 boundary and cancel is called to kill the
// command early.
//
// The caller is responsible for:
//   - Creating the command with an appropriate context (e.g., WithTimeout)
//   - Setting cmd.SysProcAttr for process group isolation
//   - Setting cmd.Cancel for platform-specific process killing
//
// cancel is called when output is truncated to stop the command. It should
// be the context.CancelFunc from the context used to create the command.
// timeout is used only for error messages.
func RunCommand(cmd *exec.Cmd, cancel context.CancelFunc, parentCtx, childCtx context.Context, maxOutputBytes int, timeout time.Duration) (RunResult, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start: %w", err)
	}

	// Read stdout and stderr concurrently to avoid deadlock when
	// the command fills one pipe buffer while we are reading the other.
	var stderrBuf bytes.Buffer

	var wg sync.WaitGroup

	wg.Go(func() {
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	})

	limitReader := &io.LimitedReader{R: stdoutPipe, N: int64(maxOutputBytes) + 1}

	var buf bytes.Buffer

	_, _ = io.Copy(&buf, limitReader)

	truncated := limitReader.N <= 0

	// If output was truncated, kill the command immediately rather than
	// waiting for it to finish producing output we will discard.
	if truncated {
		cancel()
	}

	// Ensure stderr goroutine finishes before reading stderrBuf.
	wg.Wait()

	waitErr := cmd.Wait()

	stdout := buf.String()
	if truncated {
		stdout = TruncateUTF8(stdout, maxOutputBytes)
	}

	stderrStr := stderrBuf.String()

	if waitErr != nil {
		// If we truncated and killed the command, treat it as a successful truncation.
		if truncated {
			return RunResult{Stdout: stdout, Stderr: stderrStr, Truncated: true}, nil
		}

		if parentCtx.Err() == context.Canceled {
			return RunResult{}, fmt.Errorf("command cancelled")
		}

		if childCtx.Err() == context.DeadlineExceeded {
			return RunResult{}, fmt.Errorf("command timed out after %s", timeout)
		}

		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return RunResult{Stdout: stdout, Stderr: stderrStr, ExitCode: exitErr.ExitCode()}, nil
		}

		return RunResult{}, waitErr
	}

	return RunResult{Stdout: stdout, Stderr: stderrStr, Truncated: truncated}, nil
}
