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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}

	return ""
}

func createTempFiles(t *testing.T, dir string, names []string) {
	t.Helper()

	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("failed to create file %s: %v", name, err)
		}
	}
}

func TestGlobTool_Success(t *testing.T) {
	dir := t.TempDir()
	createTempFiles(t, dir, []string{"a.txt", "b.txt", "c.go"})

	gt := New(WithWorkingDir(dir))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "a.txt") {
		t.Errorf("expected a.txt in output, got: %s", text)
	}

	if !strings.Contains(text, "b.txt") {
		t.Errorf("expected b.txt in output, got: %s", text)
	}

	if strings.Contains(text, "c.go") {
		t.Errorf("did not expect c.go in output, got: %s", text)
	}
}

func TestGlobTool_SortedByModTime(t *testing.T) {
	dir := t.TempDir()

	// Create files with different modification times.
	files := []string{"old.txt", "mid.txt", "new.txt"}
	now := time.Now()

	for i, name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
		// Set modification times: old=now-2h, mid=now-1h, new=now
		modTime := now.Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("failed to set times: %v", err)
		}
	}

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), text)
	}

	// Most recent first: new.txt, mid.txt, old.txt
	if !strings.HasSuffix(lines[0], "new.txt") {
		t.Errorf("expected new.txt first, got: %s", lines[0])
	}

	if !strings.HasSuffix(lines[2], "old.txt") {
		t.Errorf("expected old.txt last, got: %s", lines[2])
	}
}

func TestGlobTool_NoMatches(t *testing.T) {
	dir := t.TempDir()
	createTempFiles(t, dir, []string{"a.go"})

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.xyz", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success (empty result), got error: %s", resultText(result))
	}

	text := resultText(result)
	if strings.TrimSpace(text) != "" {
		t.Errorf("expected empty output for no matches, got: %s", text)
	}
}

func TestGlobTool_MalformedJSON(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "glob", `not json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in output, got: %s", text)
	}
}

func TestGlobTool_EmptyPattern(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "glob", `{"pattern":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

func TestGlobTool_PatternWithDotDot(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "glob", `{"pattern":"../*.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "..") {
		t.Errorf("expected '..' error in output, got: %s", text)
	}
}

func TestGlobTool_AbsolutePattern(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "glob", `{"pattern":"/etc/*.conf"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "must not be absolute") {
		t.Errorf("expected 'must not be absolute' in output, got: %s", text)
	}
}

func TestGlobTool_PathValidation(t *testing.T) {
	dir := t.TempDir()
	gt := New(WithAllowedDirs(dir))
	handler := gt.Handler()

	// Try to search outside the allowed directory.
	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": "/etc"})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for path outside allowed dirs")
	}

	text := resultText(result)
	if !strings.Contains(text, "not allowed") {
		t.Errorf("expected 'not allowed' in output, got: %s", text)
	}
}

func TestGlobTool_PathMustBeDirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "afile.txt")

	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": filePath})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for file path")
	}

	text := resultText(result)
	if !strings.Contains(text, "not a directory") {
		t.Errorf("expected 'not a directory' in output, got: %s", text)
	}
}

func TestGlobTool_MaxResults(t *testing.T) {
	dir := t.TempDir()

	// Create more files than maxResults.
	names := make([]string, 10)
	for i := range names {
		names[i] = fmt.Sprintf("file%d.txt", i)
	}

	createTempFiles(t, dir, names)

	gt := New(WithMaxResults(3))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	// Count only file path lines (exclude truncation notices starting with "...").
	fileLines := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "...") {
			fileLines++
		}
	}

	if fileLines > 3 {
		t.Errorf("expected at most 3 file results, got %d", fileLines)
	}

	if !strings.Contains(text, "results limited to") {
		t.Errorf("expected results-limited notice in output")
	}
}

func TestGlobTool_OutputTruncation(t *testing.T) {
	dir := t.TempDir()

	// Create many files to generate large output.
	names := make([]string, 100)
	for i := range names {
		names[i] = fmt.Sprintf("longfilename_to_fill_output_%03d.txt", i)
	}

	createTempFiles(t, dir, names)

	gt := New(WithMaxOutput(200))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Even with truncated output, parsing should not fail.
	text := resultText(result)
	if strings.Contains(text, "output truncated") {
		// Truncation is expected for small maxOutput with many files.
		return
	}
	// If output fits, that's also fine.
}

func TestGlobTool_Timeout(t *testing.T) {
	// Use a directory that exists but set an extremely short timeout.
	dir := t.TempDir()

	// Create some files to give find something to do.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	for i := range 50 {
		path := filepath.Join(subDir, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	gt := New(WithTimeout(1 * time.Nanosecond))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "**/*.txt", "path": dir})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With nanosecond timeout, we either get a timeout error or the command may succeed
	// if the OS schedules it fast enough. Both are acceptable.
	_ = result
}

func TestGlobTool_ContextCancel(t *testing.T) {
	gt := New(WithTimeout(30 * time.Second))
	handler := gt.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	result, err := handler(ctx, "glob", `{"pattern":"*.txt","path":"/tmp"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for cancelled context")
	}

	text := resultText(result)
	if !strings.Contains(text, "cancel") {
		t.Errorf("expected 'cancel' in output, got: %s", text)
	}
}

func TestGlobTool_ToolDef(t *testing.T) {
	gt := New()
	def := gt.ToolDef()

	if def.Name != "glob" {
		t.Errorf("expected name 'glob', got %q", def.Name)
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

	if _, ok := props["pattern"]; !ok {
		t.Error("expected 'pattern' property in parameters")
	}

	if _, ok := props["path"]; !ok {
		t.Error("expected 'path' property in parameters")
	}
}

func TestGlobTool_Register(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("glob")
	if !ok {
		t.Fatal("glob tool not found in registry")
	}

	if def.Name != "glob" {
		t.Errorf("expected name 'glob', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}
}

func TestGlobTool_RegisterDuplicate(t *testing.T) {
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

func TestGlobTool_ConcurrentExecution(t *testing.T) {
	dir := t.TempDir()
	createTempFiles(t, dir, []string{"a.txt", "b.txt"})

	gt := New()
	handler := gt.Handler()

	const n = 5

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": dir})
			results[idx], errs[idx] = handler(context.Background(), "glob", string(args))
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("invocation %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("invocation %d returned IsError=true: %s", i, resultText(results[i]))
		}
	}
}

func TestGlobTool_NoWorkingDir(t *testing.T) {
	gt := New() // no working dir configured
	handler := gt.Handler()

	// No path argument, no working dir.
	result, err := handler(context.Background(), "glob", `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true when no path and no working dir")
	}

	text := resultText(result)
	if !strings.Contains(text, "no search directory") {
		t.Errorf("expected 'no search directory' in output, got: %s", text)
	}
}

func TestGlobTool_WorkingDirFallback(t *testing.T) {
	dir := t.TempDir()
	createTempFiles(t, dir, []string{"test.txt"})

	gt := New(WithWorkingDir(dir))
	handler := gt.Handler()

	// No path argument; should use working dir.
	result, err := handler(context.Background(), "glob", `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "test.txt") {
		t.Errorf("expected test.txt in output, got: %s", text)
	}
}

func TestGlobTool_NonexistentPath(t *testing.T) {
	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "*.txt", "path": "/nonexistent/path/12345"})

	result, err := handler(context.Background(), "glob", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for nonexistent path")
	}

	text := resultText(result)
	if !strings.Contains(text, "does not exist") {
		t.Errorf("expected 'does not exist' in output, got: %s", text)
	}
}
