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
	"strings"
	"testing"
)

func TestValidatePath_UNCBackslash(t *testing.T) {
	_, err := ValidatePath("test", `\\server\share\file.txt`, nil)
	if err == nil {
		t.Fatal("expected error for UNC backslash path")
	}

	if !strings.Contains(err.Error(), "UNC paths are not allowed") {
		t.Errorf("expected 'UNC paths are not allowed' in error, got: %v", err)
	}
}

func TestValidatePath_UNCSlash(t *testing.T) {
	_, err := ValidatePath("test", "//server/share/file.txt", nil)
	if err == nil {
		t.Fatal("expected error for UNC slash path")
	}

	if !strings.Contains(err.Error(), "UNC paths are not allowed") {
		t.Errorf("expected 'UNC paths are not allowed' in error, got: %v", err)
	}
}

func TestValidatePath_TripleSlashNotRejected(t *testing.T) {
	// ///foo/bar should not be rejected as UNC; filepath.Clean handles it.
	_, err := ValidatePath("test", "///foo/bar", nil)
	if err != nil && strings.Contains(err.Error(), "UNC") {
		t.Errorf("triple-slash path should not be rejected as UNC: %v", err)
	}
}

func TestValidatePath_NormalAbsolutePath(t *testing.T) {
	dir := t.TempDir()

	cleaned, err := ValidatePath("test", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cleaned == "" {
		t.Error("expected non-empty cleaned path")
	}
}

func TestValidatePath_EmptyPath(t *testing.T) {
	_, err := ValidatePath("test", "", nil)
	if err == nil {
		t.Fatal("expected error for empty path")
	}

	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected 'must not be empty' in error, got: %v", err)
	}
}

func TestValidatePath_RelativePath(t *testing.T) {
	_, err := ValidatePath("test", "relative/path.txt", nil)
	if err == nil {
		t.Fatal("expected error for relative path")
	}

	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("expected 'must be absolute' in error, got: %v", err)
	}
}

func TestValidatePath_AllowedDirs(t *testing.T) {
	allowedDir := t.TempDir()
	otherDir := t.TempDir()

	// Create a file in otherDir.
	path := WriteTestFile(t, otherDir, "test.txt", "content")

	_, err := ValidatePath("test", path, CleanAllowedDirs([]string{allowedDir}))
	if err == nil {
		t.Fatal("expected error for path outside allowed dirs")
	}

	if !strings.Contains(err.Error(), "path not allowed") {
		t.Errorf("expected 'path not allowed' in error, got: %v", err)
	}
}

func TestValidatePath_AllowedDirsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := WriteTestFile(t, dir, "test.txt", "content")

	resolved := ResolveDir(t, dir)
	cleaned, err := ValidatePath("test", path, CleanAllowedDirs([]string{resolved}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cleaned == "" {
		t.Error("expected non-empty cleaned path")
	}
}
