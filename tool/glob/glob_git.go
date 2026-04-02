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
	"os"
	"os/exec"
	"path/filepath"
)

// isGitRepo checks whether dir (or any ancestor) is inside a git repository.
func isGitRepo(dir string) bool {
	// Walk up until we find .git or reach the root.
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}

		dir = parent
	}
}

// buildGitCommand builds a git ls-files command that respects .gitignore.
// It lists tracked files plus untracked-but-not-ignored files matching the pattern.
// Returns nil if git is not available.
func buildGitCommand(ctx context.Context, dir, pattern string) *exec.Cmd {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil
	}

	// git ls-files with --cached (tracked) and --others --exclude-standard (untracked but not ignored).
	// The pattern is passed directly — git ls-files supports glob patterns.
	cmd := exec.CommandContext(ctx, gitPath, "-C", dir,
		"ls-files",
		"--cached",
		"--others",
		"--exclude-standard",
		pattern,
	)

	return cmd
}
