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

package read

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

func TestReadTool_Success(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello\nworld\n")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "hello\nworld" {
		t.Errorf("expected %q, got %q", "hello\nworld", got)
	}
}

func TestReadTool_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := toolkit.WriteTestFile(t, dir, "test.txt", strings.Join(lines, "\n"))

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"offset":3,"limit":3}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "line3\nline4\nline5" {
		t.Errorf("expected %q, got %q", "line3\nline4\nline5", got)
	}
}

func TestReadTool_OffsetOnly(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := toolkit.WriteTestFile(t, dir, "test.txt", strings.Join(lines, "\n"))

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"offset":4}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "line4\nline5" {
		t.Errorf("expected %q, got %q", "line4\nline5", got)
	}
}

func TestReadTool_LimitOnly(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := toolkit.WriteTestFile(t, dir, "test.txt", strings.Join(lines, "\n"))

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"limit":3}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "line1\nline2\nline3" {
		t.Errorf("expected %q, got %q", "line1\nline2\nline3", got)
	}
}

func TestReadTool_OffsetBeyondFileLength(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "line1\nline2\n")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"offset":100}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"/tmp/nonexistent_readtool_test_file.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file does not exist") {
		t.Errorf("expected 'file does not exist' in output, got: %s", text)
	}
}

func TestReadTool_DirectoryPath(t *testing.T) {
	dir := t.TempDir()

	// Create some files in the directory.
	for _, name := range []string{"alpha.txt", "beta.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatal("expected IsError=false for directory listing")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "Directory:") {
		t.Errorf("expected 'Directory:' header, got: %s", text)
	}

	if !strings.Contains(text, "alpha.txt") || !strings.Contains(text, "beta.txt") {
		t.Errorf("expected file names in listing, got: %s", text)
	}

	if !strings.Contains(text, "subdir/") {
		t.Errorf("expected directory suffix '/', got: %s", text)
	}
}

func TestReadTool_EmptyPath(t *testing.T) {
	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", `{"file_path":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

func TestReadTool_RelativePath(t *testing.T) {
	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"relative/path.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must be absolute") {
		t.Errorf("expected 'must be absolute' in output, got: %s", text)
	}
}

func TestReadTool_MalformedJSON(t *testing.T) {
	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", `not json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in output, got: %s", text)
	}
}

func TestReadTool_NegativeOffset(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello\n")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"offset":-1}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "offset must be >= 1") {
		t.Errorf("expected 'offset must be >= 1' in output, got: %s", text)
	}
}

func TestReadTool_NegativeLimit(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello\n")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"limit":-1}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "limit must be >= 1") {
		t.Errorf("expected 'limit must be >= 1' in output, got: %s", text)
	}
}

func TestReadTool_LargeFileTruncation(t *testing.T) {
	dir := t.TempDir()

	maxBytes := 100
	content := strings.Repeat("A", 200)
	path := toolkit.WriteTestFile(t, dir, "large.txt", content)

	rt := New(WithMaxReadBytes(maxBytes))
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "output truncated") {
		t.Errorf("expected 'output truncated' in output, got: %s", text)
	}

	if !strings.Contains(text, fmt.Sprintf("first %d bytes", maxBytes)) {
		t.Errorf("expected truncation size in output, got: %s", text)
	}
}

func TestReadTool_ContextCancel(t *testing.T) {
	rt := New()
	handler := rt.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := handler(ctx, "", `{"file_path":"/tmp/whatever.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "context canceled") {
		t.Errorf("expected 'context canceled' in output, got: %s", text)
	}
}

func TestReadTool_ShowLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "alpha\nbeta\ngamma")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"show_line_numbers":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	expected := "1\talpha\n2\tbeta\n3\tgamma"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestReadTool_ShowLineNumbersWithOffset(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := toolkit.WriteTestFile(t, dir, "test.txt", strings.Join(lines, "\n"))

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"offset":3,"limit":2,"show_line_numbers":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	expected := "3\tline3\n4\tline4"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestReadTool_ToolDef(t *testing.T) {
	rt := New()
	def := rt.ToolDef()

	if def.Name != "read" {
		t.Errorf("expected name 'read', got %q", def.Name)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	params, ok := def.Parameters.(map[string]any)
	if !ok {
		t.Fatal("expected Parameters to be map[string]any")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in parameters")
	}

	for _, prop := range []string{"file_path", "offset", "limit", "show_line_numbers"} {
		if _, ok := props[prop]; !ok {
			t.Errorf("expected %q property in parameters", prop)
		}
	}
}

func TestReadTool_Register(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("read")
	if !ok {
		t.Fatal("read tool not found in registry")
	}

	if def.Name != "read" {
		t.Errorf("expected name 'read', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}
}

func TestReadTool_RegisterDuplicate(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	err := Register(reg)
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}

	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("expected 'already registered' error, got: %v", err)
	}
}

func TestReadTool_AllowedDirs(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	path := toolkit.WriteTestFile(t, otherDir, "secret.txt", "secret data")

	rt := New(WithAllowedDirs(dir))
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "path not allowed") {
		t.Errorf("expected 'path not allowed' in output, got: %s", text)
	}
}

func TestReadTool_AllowedDirsSymlink(t *testing.T) {
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()
	toolkit.WriteTestFile(t, outsideDir, "secret.txt", "secret data")

	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	targetPath := filepath.Join(symlinkPath, "secret.txt")

	rt := New(WithAllowedDirs(allowedDir))
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, targetPath))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for symlink escape")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "path not allowed") && !strings.Contains(text, "symlink resolves outside") {
		t.Errorf("expected path rejection in output, got: %s", text)
	}
}

func TestReadTool_Concurrent(t *testing.T) {
	dir := t.TempDir()

	const n = 10

	paths := make([]string, n)
	for i := range n {
		paths[i] = toolkit.WriteTestFile(t, dir, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content%d", i))
	}

	rt := New()
	handler := rt.Handler()

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			args := fmt.Sprintf(`{"file_path":%q}`, paths[idx])
			results[idx], errs[idx] = handler(context.Background(), "", args)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("read %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("read %d returned IsError=true: %s", i, toolkit.ResultText(results[i]))
		}

		expected := fmt.Sprintf("content%d", i)
		if got := toolkit.ResultText(results[i]); got != expected {
			t.Errorf("read %d: expected %q, got %q", i, expected, got)
		}
	}
}

func TestReadTool_ReadTrackerRecords(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello\n")

	tracker := toolkit.NewMemoryReadTracker(0)
	rt := New(WithReadTracker(tracker))
	handler := rt.Handler()

	// Before reading, tracker should not have the file.
	cleanedPath, err := toolkit.ValidatePath("test", path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tracker.HasRead(cleanedPath) {
		t.Error("expected HasRead to return false before reading")
	}

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	// After reading, tracker should have the file.
	if !tracker.HasRead(cleanedPath) {
		t.Error("expected HasRead to return true after reading")
	}
}

func TestReadTool_ReadTrackerNotCalledForDir(t *testing.T) {
	dir := t.TempDir()

	// Create a file so the directory is not empty.
	toolkit.WriteTestFile(t, dir, "file.txt", "content")

	tracker := toolkit.NewMemoryReadTracker(0)
	rt := New(WithReadTracker(tracker))
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	// Directory listing should not record a read.
	cleanedDir, err := toolkit.ValidatePath("test", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tracker.HasRead(cleanedDir) {
		t.Error("expected HasRead to return false for directory listing")
	}
}

func TestReadTool_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "empty.txt", "")

	rt := New()
	handler := rt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	got := toolkit.ResultText(result)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
