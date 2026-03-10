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

package writetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

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

func TestWriteTool_CreateNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":"hello world"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "wrote 11 bytes") {
		t.Errorf("expected 'wrote 11 bytes' in output, got: %s", text)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", string(content))
	}
}

func TestWriteTool_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "existing.txt", "old content")

	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":"new content"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "new content" {
		t.Errorf("expected %q, got %q", "new content", string(content))
	}
}

func TestWriteTool_CreateParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":"deep content"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "deep content" {
		t.Errorf("expected %q, got %q", "deep content", string(content))
	}
}

func TestWriteTool_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":""}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(content) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(content))
	}
}

func TestWriteTool_EmptyPath(t *testing.T) {
	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"","content":"data"}`)
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

func TestWriteTool_RelativePath(t *testing.T) {
	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"relative/path.txt","content":"data"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "must be absolute") {
		t.Errorf("expected 'must be absolute' in output, got: %s", text)
	}
}

func TestWriteTool_MalformedJSON(t *testing.T) {
	wt := New()
	handler := wt.Handler()

	result, err := handler(context.Background(), "", `not json`)
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

func TestWriteTool_ExceedsMaxWriteBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	wt := New(WithMaxWriteBytes(10))
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":"this content is way too long"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' in output, got: %s", text)
	}
}

func TestWriteTool_ToolDef(t *testing.T) {
	wt := New()
	def := wt.ToolDef()

	if def.Name != "file_write" {
		t.Errorf("expected name 'file_write', got %q", def.Name)
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

	if _, ok := props["file_path"]; !ok {
		t.Error("expected 'file_path' property in parameters")
	}

	if _, ok := props["content"]; !ok {
		t.Error("expected 'content' property in parameters")
	}
}

func TestWriteTool_Register(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("file_write")
	if !ok {
		t.Fatal("file_write tool not found in registry")
	}

	if def.Name != "file_write" {
		t.Errorf("expected name 'file_write', got %q", def.Name)
	}
}

func TestWriteTool_RegisterDuplicate(t *testing.T) {
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

func TestWriteTool_AllowedDirs(t *testing.T) {
	allowedDir := t.TempDir()
	otherDir := t.TempDir()
	path := filepath.Join(otherDir, "forbidden.txt")

	wt := New(WithAllowedDirs(allowedDir))
	handler := wt.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(`{"file_path":%q,"content":"data"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "path not allowed") {
		t.Errorf("expected 'path not allowed' in output, got: %s", text)
	}
}

func TestWriteTool_Concurrent(t *testing.T) {
	dir := t.TempDir()

	const n = 10

	wt := New()
	handler := wt.Handler()

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			path := filepath.Join(dir, fmt.Sprintf("file%d.txt", idx))
			content := fmt.Sprintf("content%d", idx)
			args := fmt.Sprintf(`{"file_path":%q,"content":%q}`, path, content)
			results[idx], errs[idx] = handler(context.Background(), "", args)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("write %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("write %d returned IsError=true: %s", i, resultText(results[i]))
		}

		path := filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
		content, readErr := os.ReadFile(path)

		if readErr != nil {
			t.Errorf("write %d: failed to read file: %v", i, readErr)

			continue
		}

		expected := fmt.Sprintf("content%d", i)
		if string(content) != expected {
			t.Errorf("write %d: expected %q, got %q", i, expected, string(content))
		}
	}
}

func TestWriteTool_ContextCancel(t *testing.T) {
	wt := New()
	handler := wt.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := handler(ctx, "", `{"file_path":"/tmp/whatever.txt","content":"data"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "context canceled") {
		t.Errorf("expected 'context canceled' in output, got: %s", text)
	}
}
