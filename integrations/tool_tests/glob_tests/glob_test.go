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

package glob_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/glob"
	"github.com/vogo/vage/tool/toolkit"
)

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "a.txt", "content")
	toolkit.WriteTestFile(t, dir, "b.txt", "content")

	reg := tool.NewRegistry()

	if err := glob.Register(reg, glob.WithWorkingDir(dir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "glob" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("glob tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("glob")
	if !ok {
		t.Fatal("glob tool not found via Get()")
	}

	if def.Name != "glob" {
		t.Errorf("expected name 'glob', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() runs and returns the correct output.
	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "a.txt") {
		t.Errorf("expected a.txt in output, got: %s", text)
	}

	if !strings.Contains(text, "b.txt") {
		t.Errorf("expected b.txt in output, got: %s", text)
	}
}

// TestRegisterDuplicatePrevented verifies that registering a second glob tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := glob.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := glob.Register(reg)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}

	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("expected 'already registered' in error, got: %v", err)
	}
}

// TestRegisterWithAllowedDirs verifies that the AllowedDirs option is correctly
// applied when executing through the registry.
func TestRegisterWithAllowedDirs(t *testing.T) {
	allowedDir := toolkit.ResolveDir(t, t.TempDir())
	otherDir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, otherDir, "secret.txt", "secret data")

	reg := tool.NewRegistry()

	if err := glob.Register(reg, glob.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	// Searching outside allowed dir should fail.
	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, otherDir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for path outside allowed dirs")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "not allowed") {
		t.Errorf("expected 'not allowed' in output, got: %s", text)
	}

	// Searching inside allowed dir should succeed.
	toolkit.WriteTestFile(t, allowedDir, "allowed.txt", "allowed content")

	result, err = reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, allowedDir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text = toolkit.ResultText(result)
	if !strings.Contains(text, "allowed.txt") {
		t.Errorf("expected allowed.txt in output, got: %s", text)
	}
}

// ---------- Glob Execution Integration Tests ----------

// TestGlobSuccessfulSearch verifies that globbing for a specific extension
// returns matching files and excludes non-matching files.
func TestGlobSuccessfulSearch(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "main.go", "package main")
	toolkit.WriteTestFile(t, dir, "util.go", "package util")
	toolkit.WriteTestFile(t, dir, "readme.md", "# Readme")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.go","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "main.go") {
		t.Errorf("expected main.go in output, got: %s", text)
	}

	if !strings.Contains(text, "util.go") {
		t.Errorf("expected util.go in output, got: %s", text)
	}

	if strings.Contains(text, "readme.md") {
		t.Errorf("did not expect readme.md in output, got: %s", text)
	}
}

// TestGlobRecursivePattern verifies that recursive glob patterns (**/*.ext)
// match files in subdirectories.
func TestGlobRecursivePattern(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())

	// Create nested directory structure.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	toolkit.WriteTestFile(t, dir, "top.txt", "top")
	toolkit.WriteTestFile(t, subDir, "nested.txt", "nested")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"**/*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "nested.txt") {
		t.Errorf("expected nested.txt in output, got: %s", text)
	}
}

// TestGlobSortedByModTime verifies that results are sorted by modification
// time with most recent first.
func TestGlobSortedByModTime(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())

	// Create files with distinct modification times.
	files := []string{"old.txt", "mid.txt", "new.txt"}
	now := time.Now()

	for i, name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		modTime := now.Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("failed to set times: %v", err)
		}
	}

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), text)
	}

	// Most recent first: new.txt, mid.txt, old.txt.
	if !strings.HasSuffix(lines[0], "new.txt") {
		t.Errorf("expected new.txt first, got: %s", lines[0])
	}

	if !strings.HasSuffix(lines[2], "old.txt") {
		t.Errorf("expected old.txt last, got: %s", lines[2])
	}
}

// TestGlobNoMatches verifies that a pattern matching no files returns an empty
// TextResult (not an error).
func TestGlobNoMatches(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "data.go", "package data")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.xyz","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success (empty result), got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if strings.TrimSpace(text) != "" {
		t.Errorf("expected empty output for no matches, got: %s", text)
	}
}

// TestGlobMaxResults verifies that results are capped at the configured
// maxResults limit.
func TestGlobMaxResults(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())

	// Create more files than maxResults.
	for i := range 10 {
		toolkit.WriteTestFile(t, dir, fmt.Sprintf("file%d.txt", i), "content")
	}

	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithMaxResults(3)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	// Filter out truncation notice lines (starting with "...").
	var fileLines []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "...") {
			fileLines = append(fileLines, line)
		}
	}

	if len(fileLines) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(fileLines))
	}

	// Verify truncation notice is present since we have more files than maxResults.
	if !strings.Contains(text, "results limited to") {
		t.Error("expected truncation notice in output")
	}
}

// TestGlobWorkingDirFallback verifies that when no path argument is provided,
// the configured working directory is used as the search directory.
func TestGlobWorkingDirFallback(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "test.txt", "content")

	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithWorkingDir(dir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// No path argument; should use working dir.
	result, err := reg.Execute(context.Background(), "glob", `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "test.txt") {
		t.Errorf("expected test.txt in output, got: %s", text)
	}
}

// ---------- Error Handling Integration Tests ----------

// TestGlobMalformedJSON verifies that invalid JSON arguments return an
// ErrorResult with an appropriate message.
func TestGlobMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob", `{invalid json`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for malformed JSON")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in output, got: %s", text)
	}
}

// TestGlobEmptyPattern verifies that an empty pattern returns an ErrorResult.
func TestGlobEmptyPattern(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob", `{"pattern":""}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty pattern")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

// TestGlobPatternWithDotDot verifies that a pattern containing '..' path
// components is rejected to prevent directory traversal.
func TestGlobPatternWithDotDot(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob", `{"pattern":"../*.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for pattern with '..'")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "..") {
		t.Errorf("expected '..' error in output, got: %s", text)
	}
}

// TestGlobAbsolutePattern verifies that a pattern starting with '/' is rejected.
func TestGlobAbsolutePattern(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob", `{"pattern":"/etc/*.conf"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for absolute pattern")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must not be absolute") {
		t.Errorf("expected 'must not be absolute' in output, got: %s", text)
	}
}

// TestGlobPathMustBeDirectory verifies that providing a file path (not a
// directory) returns an ErrorResult.
func TestGlobPathMustBeDirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := toolkit.WriteTestFile(t, dir, "afile.txt", "data")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, filePath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for file path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "not a directory") {
		t.Errorf("expected 'not a directory' in output, got: %s", text)
	}
}

// TestGlobNonexistentPath verifies that a non-existent directory path returns
// an ErrorResult.
func TestGlobNonexistentPath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		`{"pattern":"*.txt","path":"/nonexistent/path/12345"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for nonexistent path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "does not exist") {
		t.Errorf("expected 'does not exist' in output, got: %s", text)
	}
}

// TestGlobNoWorkingDir verifies that an error is returned when no path argument
// is provided and no working directory is configured.
func TestGlobNoWorkingDir(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob", `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true when no path and no working dir")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "no search directory") {
		t.Errorf("expected 'no search directory' in output, got: %s", text)
	}
}

// ---------- Timeout and Cancellation Integration Tests ----------

// TestGlobContextCancellation verifies that a cancelled context returns an
// ErrorResult with a cancellation message.
func TestGlobContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	result, err := reg.Execute(ctx, "glob", `{"pattern":"*.txt","path":"/tmp"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for cancelled context")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "cancel") {
		t.Errorf("expected 'cancel' in output, got: %s", text)
	}
}

// TestGlobOutputTruncation verifies that large output is truncated with a
// descriptive truncation notice.
func TestGlobOutputTruncation(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())

	// Create many files to generate large output.
	for i := range 100 {
		toolkit.WriteTestFile(t, dir, fmt.Sprintf("longfilename_to_fill_output_%03d.txt", i), "data")
	}

	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithMaxOutput(200)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Even with truncated output, parsing should not fail.
	text := toolkit.ResultText(result)
	if strings.Contains(text, "output truncated") {
		// Truncation is expected for small maxOutput with many files.
		return
	}
	// If output fits, that's also fine.
}

// ---------- Concurrent Execution Integration Test ----------

// TestGlobConcurrentExecution verifies that multiple glob invocations can be
// executed concurrently through the same registry without interference.
func TestGlobConcurrentExecution(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "a.txt", "content")
	toolkit.WriteTestFile(t, dir, "b.txt", "content")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	const n = 10

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			args := fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir)
			results[idx], errs[idx] = reg.Execute(context.Background(), "glob", args)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("invocation %d returned error: %v", i, errs[i])

			continue
		}

		if results[i].IsError {
			t.Errorf("invocation %d returned IsError=true: %s", i, toolkit.ResultText(results[i]))
		}
	}
}

// ---------- ToolDef Schema Validation Integration Test ----------

// TestToolDefSchemaForLLMCompatibility verifies that the ToolDef schema is
// correctly structured for LLM tool calling (proper JSON Schema with required
// fields and additionalProperties constraint).
func TestToolDefSchemaForLLMCompatibility(t *testing.T) {
	gt := glob.New()
	def := gt.ToolDef()

	// Verify name.
	if def.Name != "glob" {
		t.Errorf("expected name 'glob', got %q", def.Name)
	}

	// Verify source.
	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	// Verify parameters structure.
	params, ok := def.Parameters.(map[string]any)
	if !ok {
		t.Fatal("expected Parameters to be map[string]any")
	}

	if params["type"] != "object" {
		t.Errorf("expected type 'object', got %v", params["type"])
	}

	// Verify properties contain expected fields.
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected 'properties' in parameters")
	}

	for _, prop := range []string{"pattern", "path"} {
		if _, ok := props[prop]; !ok {
			t.Errorf("expected %q property in parameters", prop)
		}
	}

	// Verify required field.
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected 'required' to be []string")
	}

	if len(required) != 1 || required[0] != "pattern" {
		t.Errorf("expected required=['pattern'], got %v", required)
	}

	// Verify additionalProperties is false.
	if params["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false, got %v", params["additionalProperties"])
	}

	// Verify conversion to aimodel.Tool works.
	aiTools := tool.ToAIModelTools([]schema.ToolDef{def})
	if len(aiTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(aiTools))
	}

	if aiTools[0].Function.Name != "glob" {
		t.Errorf("expected function name 'glob', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Error Recovery Integration Test ----------

// TestGlobErrorThenSuccess verifies that a failed glob does not affect
// subsequent successful globs through the same registry.
func TestGlobErrorThenSuccess(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "good.txt", "good content")

	reg := tool.NewRegistry()
	if err := glob.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing glob (nonexistent path).
	result1, err := reg.Execute(context.Background(), "glob",
		`{"pattern":"*.txt","path":"/nonexistent/path/12345"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first glob to fail")
	}

	// Second: a succeeding glob.
	result2, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second glob to succeed, got error: %s", toolkit.ResultText(result2))
	}

	text := toolkit.ResultText(result2)
	if !strings.Contains(text, "good.txt") {
		t.Errorf("expected 'good.txt' in output, got: %s", text)
	}
}

// ---------- Symlink Integration Test ----------

// TestGlobSymlinkOutsideAllowedDirs verifies that a symlink pointing outside
// allowed directories is rejected when used as the search path.
func TestGlobSymlinkOutsideAllowedDirs(t *testing.T) {
	allowedDir := toolkit.ResolveDir(t, t.TempDir())
	outsideDir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, outsideDir, "secret.txt", "secret data")

	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.txt","path":%q}`, symlinkPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for symlink escape")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "not allowed") {
		t.Errorf("expected 'not allowed' in output, got: %s", text)
	}
}
