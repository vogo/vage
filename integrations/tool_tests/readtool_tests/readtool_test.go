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

package readtool_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
	"github.com/vogo/vagent/tool/readtool"
)

// ---------- Helper ----------

func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}

	return ""
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	return path
}

// resolveDir resolves symlinks in a directory path (needed on macOS where
// /var -> /private/var).
func resolveDir(t *testing.T, dir string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("failed to resolve dir: %v", err)
	}

	return resolved
}

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "test.txt", "hello world")

	reg := tool.NewRegistry()

	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "file_read" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("file_read tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("file_read")
	if !ok {
		t.Fatal("file_read tool not found via Get()")
	}

	if def.Name != "file_read" {
		t.Errorf("expected name 'file_read', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() runs and returns the correct output.
	result, err := reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

// TestRegisterDuplicatePrevented verifies that registering a second read tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := readtool.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := readtool.Register(reg)
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
	allowedDir := resolveDir(t, t.TempDir())
	otherDir := resolveDir(t, t.TempDir())
	path := writeTestFile(t, otherDir, "secret.txt", "secret data")

	reg := tool.NewRegistry()

	if err := readtool.Register(reg, readtool.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	// Reading from outside allowed dir should fail.
	result, err := reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for path outside allowed dirs")
	}

	text := resultText(result)
	if !strings.Contains(text, "path not allowed") {
		t.Errorf("expected 'path not allowed' in output, got: %s", text)
	}

	// Reading from inside allowed dir should succeed.
	_ = writeTestFile(t, allowedDir, "allowed.txt", "allowed content")
	allowedPath := filepath.Join(allowedDir, "allowed.txt")

	result, err = reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, allowedPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "allowed content" {
		t.Errorf("expected 'allowed content', got %q", got)
	}
}

// ---------- Read Execution Integration Tests ----------

// TestReadEntireFile verifies that reading an entire file through the registry
// returns the complete content.
func TestReadEntireFile(t *testing.T) {
	dir := t.TempDir()

	content := "line1\nline2\nline3"
	path := writeTestFile(t, dir, "full.txt", content)

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != content {
		t.Errorf("expected %q, got %q", content, got)
	}
}

// TestReadWithOffsetAndLimit verifies that offset and limit parameters correctly
// select a subset of lines when executing through the registry.
func TestReadWithOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := writeTestFile(t, dir, "lines.txt", strings.Join(lines, "\n"))

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Read lines 3-5.
	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q,"offset":3,"limit":3}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "line3\nline4\nline5" {
		t.Errorf("expected 'line3\\nline4\\nline5', got %q", got)
	}
}

// TestReadEmptyFile verifies that reading an empty file returns an empty
// TextResult (not an error).
func TestReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "empty.txt", "")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestReadOffsetBeyondEnd verifies that an offset past the end of the file
// returns an empty TextResult (not an error).
func TestReadOffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "short.txt", "line1\nline2")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q,"offset":100}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestReadLargeFileTruncation verifies that reading a file exceeding
// maxReadBytes is truncated with a descriptive notice.
func TestReadLargeFileTruncation(t *testing.T) {
	dir := t.TempDir()
	maxBytes := 100
	content := strings.Repeat("A", 200)
	path := writeTestFile(t, dir, "large.txt", content)

	reg := tool.NewRegistry()
	if err := readtool.Register(reg, readtool.WithMaxReadBytes(maxBytes)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "output truncated") {
		t.Errorf("expected 'output truncated' in output, got: %s", text)
	}

	if !strings.Contains(text, fmt.Sprintf("first %d bytes", maxBytes)) {
		t.Errorf("expected truncation size in output, got: %s", text)
	}
}

// ---------- Error Handling Integration Tests ----------

// TestReadFileNotFound verifies that a non-existent path returns an ErrorResult
// with a descriptive message.
func TestReadFileNotFound(t *testing.T) {
	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		`{"file_path":"/tmp/nonexistent_readtool_integration_test.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for non-existent file")
	}

	text := resultText(result)
	if !strings.Contains(text, "file does not exist") {
		t.Errorf("expected 'file does not exist' in output, got: %s", text)
	}
}

// TestReadDirectoryPath verifies that a directory path returns an ErrorResult.
func TestReadDirectoryPath(t *testing.T) {
	dir := t.TempDir()

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for directory path")
	}

	text := resultText(result)
	if !strings.Contains(text, "directory") {
		t.Errorf("expected 'directory' in output, got: %s", text)
	}
}

// TestReadEmptyPath verifies that an empty file_path returns an ErrorResult.
func TestReadEmptyPath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read", `{"file_path":""}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty path")
	}

	text := resultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

// TestReadRelativePath verifies that a relative path returns an ErrorResult.
func TestReadRelativePath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		`{"file_path":"relative/path.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for relative path")
	}

	text := resultText(result)
	if !strings.Contains(text, "must be absolute") {
		t.Errorf("expected 'must be absolute' in output, got: %s", text)
	}
}

// TestReadMalformedJSON verifies that invalid JSON arguments return an ErrorResult.
func TestReadMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read", `{invalid json`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for malformed JSON")
	}

	text := resultText(result)
	if !strings.Contains(text, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in output, got: %s", text)
	}
}

// TestReadNegativeOffset verifies that a negative offset returns an ErrorResult.
func TestReadNegativeOffset(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "test.txt", "hello\n")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q,"offset":-1}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for negative offset")
	}

	text := resultText(result)
	if !strings.Contains(text, "offset must be >= 1") {
		t.Errorf("expected 'offset must be >= 1' in output, got: %s", text)
	}
}

// TestReadNegativeLimit verifies that a negative limit returns an ErrorResult.
func TestReadNegativeLimit(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "test.txt", "hello\n")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q,"limit":-1}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for negative limit")
	}

	text := resultText(result)
	if !strings.Contains(text, "limit must be >= 1") {
		t.Errorf("expected 'limit must be >= 1' in output, got: %s", text)
	}
}

// TestReadContextCancellation verifies that a cancelled context returns an
// ErrorResult with a descriptive message.
func TestReadContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := reg.Execute(ctx, "file_read", `{"file_path":"/tmp/whatever.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for cancelled context")
	}

	text := resultText(result)
	if !strings.Contains(text, "context canceled") {
		t.Errorf("expected 'context canceled' in output, got: %s", text)
	}
}

// TestReadSymlinkOutsideAllowedDirs verifies that a symlink pointing outside
// allowed directories is rejected.
func TestReadSymlinkOutsideAllowedDirs(t *testing.T) {
	allowedDir := resolveDir(t, t.TempDir())
	outsideDir := resolveDir(t, t.TempDir())
	writeTestFile(t, outsideDir, "secret.txt", "secret data")

	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	targetPath := filepath.Join(symlinkPath, "secret.txt")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg, readtool.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q}`, targetPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for symlink escape")
	}

	text := resultText(result)
	if !strings.Contains(text, "path not allowed") {
		t.Errorf("expected 'path not allowed' in output, got: %s", text)
	}
}

// ---------- ToolDef Schema Validation Integration Test ----------

// TestToolDefSchemaForLLMCompatibility verifies that the ToolDef schema is
// correctly structured for LLM tool calling (proper JSON Schema with required
// fields and additionalProperties constraint).
func TestToolDefSchemaForLLMCompatibility(t *testing.T) {
	rt := readtool.New()
	def := rt.ToolDef()

	// Verify name.
	if def.Name != "file_read" {
		t.Errorf("expected name 'file_read', got %q", def.Name)
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

	for _, prop := range []string{"file_path", "offset", "limit"} {
		if _, ok := props[prop]; !ok {
			t.Errorf("expected %q property in parameters", prop)
		}
	}

	// Verify required field.
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected 'required' to be []string")
	}

	if len(required) != 1 || required[0] != "file_path" {
		t.Errorf("expected required=['file_path'], got %v", required)
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

	if aiTools[0].Function.Name != "file_read" {
		t.Errorf("expected function name 'file_read', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Error Recovery Integration Test ----------

// TestReadErrorThenSuccess verifies that a failed read does not affect
// subsequent successful reads through the same registry.
func TestReadErrorThenSuccess(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "good.txt", "good content")

	reg := tool.NewRegistry()
	if err := readtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing read (file not found).
	result1, err := reg.Execute(context.Background(), "file_read",
		`{"file_path":"/tmp/nonexistent_recovery_test.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first read to fail")
	}

	// Second: a succeeding read.
	result2, err := reg.Execute(context.Background(), "file_read",
		fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second read to succeed, got error: %s", resultText(result2))
	}

	if got := resultText(result2); got != "good content" {
		t.Errorf("expected 'good content', got %q", got)
	}
}
