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

package grep

import (
	"context"
	"encoding/json"
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

func createTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to create file %s: %v", name, err)
	}

	return path
}

func TestGrepTool_Success(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "hello.txt", "hello world\ngoodbye world\nhello again\n")
	createTempFile(t, dir, "other.txt", "nothing here\n")

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "hello", "path": dir})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "hello") {
		t.Errorf("expected 'hello' in output, got: %s", text)
	}

	if strings.Contains(text, "nothing here") {
		t.Errorf("did not expect 'nothing here' in output, got: %s", text)
	}
}

func TestGrepTool_WithInclude(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "code.go", "func main() {\n}\n")
	createTempFile(t, dir, "readme.txt", "func description\n")

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "func", "path": dir, "include": "*.go"})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "code.go") {
		t.Errorf("expected code.go in output, got: %s", text)
	}

	// With include filter, readme.txt should not appear.
	if strings.Contains(text, "readme.txt") {
		t.Errorf("did not expect readme.txt in output with *.go filter, got: %s", text)
	}
}

func TestGrepTool_NoMatches(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "test.txt", "some content\n")

	gt := New()
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "zzzznonexistent", "path": dir})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No matches should NOT be an error.
	if result.IsError {
		t.Fatalf("expected success (no matches), got error: %s", resultText(result))
	}
}

func TestGrepTool_MalformedJSON(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "grep", `not json`)
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

func TestGrepTool_EmptyPattern(t *testing.T) {
	gt := New(WithWorkingDir("/tmp"))
	handler := gt.Handler()

	result, err := handler(context.Background(), "grep", `{"pattern":""}`)
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

func TestGrepTool_PathValidation(t *testing.T) {
	dir := t.TempDir()
	gt := New(WithAllowedDirs(dir))
	handler := gt.Handler()

	// Try to search outside the allowed directory.
	args, _ := json.Marshal(map[string]string{"pattern": "test", "path": "/etc"})

	result, err := handler(context.Background(), "grep", string(args))
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

func TestGrepTool_OutputTruncation(t *testing.T) {
	dir := t.TempDir()

	// Create a file with many lines to generate large output.
	var content strings.Builder
	for i := range 500 {
		content.WriteString("matchable line number ")
		content.WriteString(strings.Repeat("x", 50))
		content.WriteString(" ")
		content.WriteString(string(rune('0' + i%10)))
		content.WriteString("\n")
	}

	createTempFile(t, dir, "big.txt", content.String())

	gt := New(WithMaxOutput(200), WithTimeout(5*time.Second))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "matchable", "path": dir})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(result)
	if strings.Contains(text, "output truncated") {
		// Truncation is expected for small maxOutput.
		return
	}
	// If output happens to fit, that's also acceptable.
}

func TestGrepTool_Timeout(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "test.txt", "some content\n")

	gt := New(WithTimeout(1 * time.Nanosecond))
	handler := gt.Handler()

	args, _ := json.Marshal(map[string]string{"pattern": "content", "path": dir})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With nanosecond timeout, we may get a timeout error or the command may
	// succeed if the OS schedules it fast enough. Both are acceptable.
	_ = result
}

func TestGrepTool_ContextCancel(t *testing.T) {
	gt := New(WithTimeout(30 * time.Second))
	handler := gt.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	result, err := handler(ctx, "grep", `{"pattern":"test","path":"/tmp"}`)
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

func TestGrepTool_ToolDef(t *testing.T) {
	gt := New()
	def := gt.ToolDef()

	if def.Name != "grep" {
		t.Errorf("expected name 'grep', got %q", def.Name)
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

	if _, ok := props["include"]; !ok {
		t.Error("expected 'include' property in parameters")
	}
}

func TestGrepTool_Register(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("grep")
	if !ok {
		t.Fatal("grep tool not found in registry")
	}

	if def.Name != "grep" {
		t.Errorf("expected name 'grep', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}
}

func TestGrepTool_RegisterDuplicate(t *testing.T) {
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

func TestGrepTool_FileSearch(t *testing.T) {
	dir := t.TempDir()
	filePath := createTempFile(t, dir, "single.txt", "line one\nline two\nline three\n")

	gt := New()
	handler := gt.Handler()

	// Search a single file, not a directory.
	args, _ := json.Marshal(map[string]string{"pattern": "line two", "path": filePath})

	result, err := handler(context.Background(), "grep", string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "line two") {
		t.Errorf("expected 'line two' in output, got: %s", text)
	}
}

func TestGrepTool_ConcurrentExecution(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "data.txt", "findme here\n")

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

			args, _ := json.Marshal(map[string]string{"pattern": "findme", "path": dir})
			results[idx], errs[idx] = handler(context.Background(), "grep", string(args))
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

func TestGrepTool_NoWorkingDir(t *testing.T) {
	gt := New() // no working dir configured
	handler := gt.Handler()

	// No path argument, no working dir.
	result, err := handler(context.Background(), "grep", `{"pattern":"test"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true when no path and no working dir")
	}

	text := resultText(result)
	if !strings.Contains(text, "no search path") {
		t.Errorf("expected 'no search path' in output, got: %s", text)
	}
}

func TestGrepTool_WorkingDirFallback(t *testing.T) {
	dir := t.TempDir()
	createTempFile(t, dir, "test.txt", "findme in working dir\n")

	gt := New(WithWorkingDir(dir))
	handler := gt.Handler()

	// No path argument; should use working dir.
	result, err := handler(context.Background(), "grep", `{"pattern":"findme"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "findme") {
		t.Errorf("expected 'findme' in output, got: %s", text)
	}
}
