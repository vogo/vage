//go:build windows

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
	"strings"
)

func buildGlobCommand(ctx context.Context, dir, pattern string) (*exec.Cmd, error) {
	// Escape single quotes in dir and pattern to prevent PowerShell injection.
	// In PowerShell single-quoted strings, the only escape is '' for a literal '.
	safeDir := strings.ReplaceAll(dir, "'", "''")
	safePattern := strings.ReplaceAll(pattern, "'", "''")

	// Use PowerShell Get-ChildItem. Sorting done in Go.
	psCmd := fmt.Sprintf(
		"Get-ChildItem -Path '%s' -Recurse -Filter '%s' -File | Select-Object -ExpandProperty FullName",
		safeDir, safePattern,
	)
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)

	return cmd, nil
}

func setProcAttr(_ *exec.Cmd) {}

func setCancelFunc(_ *exec.Cmd) {}
