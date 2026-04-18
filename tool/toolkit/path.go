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

// Package toolkit provides shared utilities for file tools, including path
// validation, atomic file writes, file locking, and edit snippet generation.
package toolkit

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidatePath checks that path is non-empty, absolute, not a UNC path, and
// within allowed directories. toolName is used in error message prefixes
// (e.g., "read").
func ValidatePath(toolName, path string, allowedDirs []string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s tool: file_path must not be empty", toolName)
	}

	// Reject UNC network paths (\\server\share or //server/share).
	if strings.HasPrefix(path, `\\`) {
		return "", fmt.Errorf("%s tool: UNC paths are not allowed: %s", toolName, path)
	}

	if strings.HasPrefix(path, "//") && len(path) > 2 && path[2] != '/' {
		return "", fmt.Errorf("%s tool: UNC paths are not allowed: %s", toolName, path)
	}

	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s tool: path must be absolute: %s", toolName, path)
	}

	cleaned := filepath.Clean(path)

	if len(allowedDirs) > 0 {
		resolved, err := ResolveExistingPath(cleaned)
		if err != nil {
			return "", fmt.Errorf("%s tool: failed to resolve path: %w", toolName, err)
		}

		if !underAny(resolved, allowedDirs) {
			// Differentiate plain "outside" from "symlink escape" so the caller
			// can tell the difference: if the cleaned (unresolved) path sits
			// under an allowed dir but its symlink-resolved form does not, the
			// escape happened via a symlink component.
			if cleaned != resolved && underAny(cleaned, allowedDirs) {
				return "", fmt.Errorf("%s tool: symlink resolves outside allowed directories: %s", toolName, path)
			}
			return "", fmt.Errorf("%s tool: path not allowed: %s", toolName, path)
		}
	}

	return cleaned, nil
}

// underAny reports whether p equals or is a descendant of any of the dirs.
// Uses string prefix match; callers must pass canonical dirs (as produced by
// CleanAllowedDirs).
func underAny(p string, dirs []string) bool {
	for _, dir := range dirs {
		if p == dir {
			return true
		}

		if strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// ResolveExistingPath walks up the path until it finds an existing ancestor,
// resolves symlinks on that ancestor, then re-appends the remaining components.
func ResolveExistingPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}

	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}

	resolvedParent, err := ResolveExistingPath(parent)
	if err != nil {
		return "", err
	}

	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

// CleanAllowedDirs returns a cleaned copy of the given directory paths.
func CleanAllowedDirs(dirs []string) []string {
	cleaned := make([]string, len(dirs))
	for i, d := range dirs {
		cleaned[i] = filepath.Clean(d)
	}

	return cleaned
}
