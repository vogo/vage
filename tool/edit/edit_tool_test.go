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

package edit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/toolkit"
)

func TestEditTool_SingleReplace(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"hello","new_string":"goodbye"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "replaced 1 occurrence(s)") {
		t.Errorf("expected 'replaced 1 occurrence(s)' in output, got: %s", text)
	}

	// Verify snippet is included.
	if !strings.Contains(text, "--- snippet ---") {
		t.Errorf("expected snippet in output, got: %s", text)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "goodbye world" {
		t.Errorf("expected %q, got %q", "goodbye world", string(content))
	}
}

func TestEditTool_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "foo bar foo baz foo")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"foo","new_string":"qux","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "replaced 3 occurrence(s)") {
		t.Errorf("expected 'replaced 3 occurrence(s)' in output, got: %s", text)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "qux bar qux baz qux" {
		t.Errorf("expected %q, got %q", "qux bar qux baz qux", string(content))
	}
}

func TestEditTool_ReplaceWithEmpty(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"hello ","new_string":""}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "world" {
		t.Errorf("expected %q, got %q", "world", string(content))
	}
}

func TestEditTool_MultilineStrings(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "line1\nline2\nline3\n")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"line1\nline2","new_string":"replaced1\nreplaced2"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "replaced1\nreplaced2\nline3\n" {
		t.Errorf("expected %q, got %q", "replaced1\nreplaced2\nline3\n", string(content))
	}
}

func TestEditTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"missing","new_string":"replacement"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "old_string not found in file") {
		t.Errorf("expected 'old_string not found in file' in output, got: %s", text)
	}
}

func TestEditTool_AmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "foo bar foo baz foo")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"foo","new_string":"qux"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "matches 3 locations") {
		t.Errorf("expected 'matches 3 locations' in output, got: %s", text)
	}

	if !strings.Contains(text, "replace_all") {
		t.Errorf("expected 'replace_all' in output, got: %s", text)
	}
}

func TestEditTool_SameStrings(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"hello","new_string":"hello"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must differ") {
		t.Errorf("expected 'must differ' in output, got: %s", text)
	}
}

func TestEditTool_EmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello")

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"","new_string":"world"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "old_string must not be empty") {
		t.Errorf("expected 'old_string must not be empty' in output, got: %s", text)
	}
}

func TestEditTool_FileNotFound(t *testing.T) {
	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"/tmp/nonexistent_edittool_test.txt","old_string":"a","new_string":"b"}`)
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

func TestEditTool_DirectoryPath(t *testing.T) {
	dir := t.TempDir()
	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"a","new_string":"b"}`, dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "directory") {
		t.Errorf("expected 'directory' in output, got: %s", text)
	}
}

func TestEditTool_EmptyPath(t *testing.T) {
	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"","old_string":"a","new_string":"b"}`)
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

func TestEditTool_RelativePath(t *testing.T) {
	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"relative/path.txt","old_string":"a","new_string":"b"}`)
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

func TestEditTool_MalformedJSON(t *testing.T) {
	et := New()
	handler := et.Handler()

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

func TestEditTool_ExceedsMaxFileBytes(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("A", 200)
	path := toolkit.WriteTestFile(t, dir, "big.txt", content)

	et := New(WithMaxFileBytes(100))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"A","new_string":"B","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' in output, got: %s", text)
	}
}

func TestEditTool_ToolDef(t *testing.T) {
	et := New()
	def := et.ToolDef()

	if def.Name != "edit" {
		t.Errorf("expected name 'edit', got %q", def.Name)
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

	for _, prop := range []string{"file_path", "old_string", "new_string", "replace_all"} {
		if _, ok := props[prop]; !ok {
			t.Errorf("expected %q property in parameters", prop)
		}
	}
}

func TestEditTool_Register(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("edit")
	if !ok {
		t.Fatal("edit tool not found in registry")
	}

	if def.Name != "edit" {
		t.Errorf("expected name 'edit', got %q", def.Name)
	}
}

func TestEditTool_RegisterDuplicate(t *testing.T) {
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

func TestEditTool_AllowedDirs(t *testing.T) {
	allowedDir := t.TempDir()
	otherDir := t.TempDir()
	path := toolkit.WriteTestFile(t, otherDir, "forbidden.txt", "content")

	et := New(WithAllowedDirs(allowedDir))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"content","new_string":"replaced"}`, path))
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

func TestEditTool_Concurrent(t *testing.T) {
	dir := t.TempDir()

	const n = 10

	paths := make([]string, n)
	for i := range n {
		paths[i] = toolkit.WriteTestFile(t, dir, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("old%d content", i))
	}

	et := New()
	handler := et.Handler()

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			args := fmt.Sprintf(
				`{"file_path":%q,"old_string":"old%d","new_string":"new%d"}`,
				paths[idx], idx, idx)
			results[idx], errs[idx] = handler(context.Background(), "", args)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("edit %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("edit %d returned IsError=true: %s", i, toolkit.ResultText(results[i]))
		}

		content, readErr := os.ReadFile(paths[i])
		if readErr != nil {
			t.Errorf("edit %d: failed to read file: %v", i, readErr)

			continue
		}

		expected := fmt.Sprintf("new%d content", i)
		if string(content) != expected {
			t.Errorf("edit %d: expected %q, got %q", i, expected, string(content))
		}
	}
}

func TestEditTool_ContextCancel(t *testing.T) {
	et := New()
	handler := et.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := handler(ctx, "", `{"file_path":"/tmp/whatever.txt","old_string":"a","new_string":"b"}`)
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

func TestEditTool_ConcurrentSameFile(t *testing.T) {
	dir := t.TempDir()

	// Create a file with 10 distinct markers.
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf("marker_%d_value", i))
	}

	path := toolkit.WriteTestFile(t, dir, "shared.txt", strings.Join(lines, "\n"))

	et := New()
	handler := et.Handler()

	// Edit each marker concurrently. Because of the file lock, each edit
	// will see the result of previous edits (no lost updates).
	var wg sync.WaitGroup

	errs := make([]error, 10)
	results := make([]schema.ToolResult, 10)

	for i := range 10 {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			args := fmt.Sprintf(
				`{"file_path":%q,"old_string":"marker_%d_value","new_string":"marker_%d_done"}`,
				path, idx, idx)
			results[idx], errs[idx] = handler(context.Background(), "", args)
		}(i)
	}

	wg.Wait()

	for i := range 10 {
		if errs[i] != nil {
			t.Errorf("edit %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("edit %d returned IsError=true: %s", i, toolkit.ResultText(results[i]))
		}
	}

	// Verify all markers were replaced.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	for i := range 10 {
		expected := fmt.Sprintf("marker_%d_done", i)
		if !strings.Contains(string(content), expected) {
			t.Errorf("expected %q in file content", expected)
		}
	}
}

func TestEditTool_SnippetInResult(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}

	path := toolkit.WriteTestFile(t, dir, "test.txt", strings.Join(lines, "\n"))

	et := New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"line5","new_string":"REPLACED"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)

	// The snippet should contain surrounding lines.
	if !strings.Contains(text, "--- snippet ---") {
		t.Errorf("expected snippet header in output, got: %s", text)
	}

	if !strings.Contains(text, "REPLACED") {
		t.Errorf("expected 'REPLACED' in snippet, got: %s", text)
	}

	// Context lines should include neighbors.
	if !strings.Contains(text, "line4") {
		t.Errorf("expected 'line4' in snippet context, got: %s", text)
	}

	if !strings.Contains(text, "line6") {
		t.Errorf("expected 'line6' in snippet context, got: %s", text)
	}
}
