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

package edittool_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
	"github.com/vogo/vagent/tool/edittool"
	"github.com/vogo/vagent/tool/readtool"
	"github.com/vogo/vagent/tool/toolkit"
	"github.com/vogo/vagent/tool/writetool"
)

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	reg := tool.NewRegistry()

	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "file_edit" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("file_edit tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("file_edit")
	if !ok {
		t.Fatal("file_edit tool not found via Get()")
	}

	if def.Name != "file_edit" {
		t.Errorf("expected name 'file_edit', got %q", def.Name)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() performs the replacement correctly.
	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"hello","new_string":"goodbye"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "replaced 1 occurrence(s)") {
		t.Errorf("expected 'replaced 1 occurrence(s)' in output, got: %s", text)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "goodbye world" {
		t.Errorf("expected %q, got %q", "goodbye world", string(content))
	}
}

// TestRegisterDuplicatePrevented verifies that registering a second edit tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := edittool.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := edittool.Register(reg)
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
	forbiddenPath := toolkit.WriteTestFile(t, otherDir, "forbidden.txt", "content")

	reg := tool.NewRegistry()

	if err := edittool.Register(reg, edittool.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	// Editing outside allowed dir should fail.
	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"content","new_string":"replaced"}`, forbiddenPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for path outside allowed dirs")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "path not allowed") {
		t.Errorf("expected 'path not allowed' in output, got: %s", text)
	}

	// Editing inside allowed dir should succeed.
	allowedPath := toolkit.WriteTestFile(t, allowedDir, "allowed.txt", "old text")

	result, err = reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"old text","new_string":"new text"}`, allowedPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(allowedPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "new text" {
		t.Errorf("expected 'new text', got %q", string(content))
	}
}

// ---------- Edit Execution Integration Tests ----------

// TestEditSingleReplace verifies that a single occurrence is replaced correctly
// through the registry.
func TestEditSingleReplace(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "the quick brown fox")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"quick","new_string":"slow"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "the slow brown fox" {
		t.Errorf("expected %q, got %q", "the slow brown fox", string(content))
	}
}

// TestEditReplaceAll verifies that all occurrences are replaced when
// replace_all is set to true.
func TestEditReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "foo bar foo baz foo")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"foo","new_string":"qux","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

// TestEditReplaceWithEmpty verifies that replacing with an empty new_string
// effectively deletes the matched text.
func TestEditReplaceWithEmpty(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"hello ","new_string":""}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

// TestEditMultilineStrings verifies that old_string and new_string containing
// newlines work correctly.
func TestEditMultilineStrings(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "line1\nline2\nline3\n")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"line1\nline2","new_string":"replaced1\nreplaced2"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

// TestEditAtomicWrite verifies that the edit uses atomic write-back (the file
// is fully replaced, not partially written).
func TestEditAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	original := "line1\nline2\nline3"
	path := toolkit.WriteTestFile(t, dir, "atomic.txt", original)

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"line2","new_string":"REPLACED"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	// Verify no temp files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read directory: %v", err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".atomic-") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "line1\nREPLACED\nline3" {
		t.Errorf("expected %q, got %q", "line1\nREPLACED\nline3", string(content))
	}
}

// ---------- Error Handling Integration Tests ----------

// TestEditAmbiguousMatch verifies that multiple matches without replace_all
// returns an ErrorResult with the match count.
func TestEditAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "foo bar foo baz foo")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"foo","new_string":"qux"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for ambiguous match")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "matches 3 locations") {
		t.Errorf("expected 'matches 3 locations' in output, got: %s", text)
	}

	if !strings.Contains(text, "replace_all") {
		t.Errorf("expected 'replace_all' in output, got: %s", text)
	}

	// Verify the file was not modified.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "foo bar foo baz foo" {
		t.Errorf("file should not have been modified, got %q", string(content))
	}
}

// TestEditNotFound verifies that old_string not found in the file returns
// an ErrorResult.
func TestEditNotFound(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"missing","new_string":"replacement"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for not found")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "old_string not found in file") {
		t.Errorf("expected 'old_string not found in file' in output, got: %s", text)
	}
}

// TestEditSameStrings verifies that old_string == new_string returns an ErrorResult.
func TestEditSameStrings(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"hello","new_string":"hello"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for same strings")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must differ") {
		t.Errorf("expected 'must differ' in output, got: %s", text)
	}
}

// TestEditEmptyOldString verifies that an empty old_string returns an ErrorResult.
func TestEditEmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"","new_string":"world"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty old_string")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "old_string must not be empty") {
		t.Errorf("expected 'old_string must not be empty' in output, got: %s", text)
	}
}

// TestEditFileNotFound verifies that editing a non-existent file returns
// an ErrorResult.
func TestEditFileNotFound(t *testing.T) {
	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		`{"file_path":"/tmp/nonexistent_edittool_integration_test.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for non-existent file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file does not exist") {
		t.Errorf("expected 'file does not exist' in output, got: %s", text)
	}
}

// TestEditDirectoryPath verifies that a directory path returns an ErrorResult.
func TestEditDirectoryPath(t *testing.T) {
	dir := t.TempDir()

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"a","new_string":"b"}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for directory path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "directory") {
		t.Errorf("expected 'directory' in output, got: %s", text)
	}
}

// TestEditEmptyPath verifies that an empty file_path returns an ErrorResult.
func TestEditEmptyPath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		`{"file_path":"","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

// TestEditRelativePath verifies that a relative path returns an ErrorResult.
func TestEditRelativePath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		`{"file_path":"relative/path.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for relative path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must be absolute") {
		t.Errorf("expected 'must be absolute' in output, got: %s", text)
	}
}

// TestEditMalformedJSON verifies that invalid JSON arguments return an ErrorResult.
func TestEditMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit", `{invalid json`)
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

// TestEditExceedsMaxFileBytes verifies that a file exceeding the max size
// returns an ErrorResult.
func TestEditExceedsMaxFileBytes(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("A", 200)
	path := toolkit.WriteTestFile(t, dir, "big.txt", content)

	reg := tool.NewRegistry()
	if err := edittool.Register(reg, edittool.WithMaxFileBytes(100)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"A","new_string":"B","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for file exceeding max size")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' in output, got: %s", text)
	}
}

// TestEditContextCancellation verifies that a cancelled context returns an
// ErrorResult with a descriptive message.
func TestEditContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := reg.Execute(ctx, "file_edit",
		`{"file_path":"/tmp/whatever.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for cancelled context")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "context canceled") {
		t.Errorf("expected 'context canceled' in output, got: %s", text)
	}
}

// ---------- ToolDef Schema Validation Integration Test ----------

// TestToolDefSchemaForLLMCompatibility verifies that the ToolDef schema is
// correctly structured for LLM tool calling.
func TestToolDefSchemaForLLMCompatibility(t *testing.T) {
	et := edittool.New()
	def := et.ToolDef()

	// Verify name.
	if def.Name != "file_edit" {
		t.Errorf("expected name 'file_edit', got %q", def.Name)
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

	for _, prop := range []string{"file_path", "old_string", "new_string", "replace_all"} {
		if _, ok := props[prop]; !ok {
			t.Errorf("expected %q property in parameters", prop)
		}
	}

	// Verify required fields.
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected 'required' to be []string")
	}

	requiredSet := make(map[string]bool)
	for _, r := range required {
		requiredSet[r] = true
	}

	if !requiredSet["file_path"] || !requiredSet["old_string"] || !requiredSet["new_string"] {
		t.Errorf("expected required to contain 'file_path', 'old_string', 'new_string', got %v", required)
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

	if aiTools[0].Function.Name != "file_edit" {
		t.Errorf("expected function name 'file_edit', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Error Recovery Integration Test ----------

// TestEditErrorThenSuccess verifies that a failed edit does not affect
// subsequent successful edits through the same registry.
func TestEditErrorThenSuccess(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "good.txt", "original content")

	reg := tool.NewRegistry()
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing edit (old_string not found).
	result1, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"nonexistent","new_string":"something"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first edit to fail")
	}

	// Second: a succeeding edit.
	result2, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"original","new_string":"modified"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second edit to succeed, got error: %s", toolkit.ResultText(result2))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "modified content" {
		t.Errorf("expected 'modified content', got %q", string(content))
	}
}

// ---------- Combined Workflow Integration Test ----------

// TestCombinedReadWriteEditWorkflow verifies that all three file tools can
// work together in a combined workflow: write a file, read it, edit it,
// then read it again to verify.
func TestCombinedReadWriteEditWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.txt")

	reg := tool.NewRegistry()

	// Register all three tools by importing and registering each.
	if err := edittool.Register(reg); err != nil {
		t.Fatalf("Register edit failed: %v", err)
	}

	// Step 1: Create the file using write tool handler directly.
	writeT := writetool.New()
	writeHandler := writeT.Handler()

	writeResult, err := writeHandler(context.Background(), "",
		fmt.Sprintf(`{"file_path":%q,"content":"hello world from workflow"}`, path))
	if err != nil {
		t.Fatalf("write returned error: %v", err)
	}

	if writeResult.IsError {
		t.Fatalf("write failed: %s", toolkit.ResultText(writeResult))
	}

	// Step 2: Read the file using read tool handler directly.
	readT := readtool.New()
	readHandler := readT.Handler()

	readResult, err := readHandler(context.Background(), "",
		fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("read returned error: %v", err)
	}

	if readResult.IsError {
		t.Fatalf("read failed: %s", toolkit.ResultText(readResult))
	}

	if got := toolkit.ResultText(readResult); got != "hello world from workflow" {
		t.Errorf("expected 'hello world from workflow', got %q", got)
	}

	// Step 3: Edit the file using the registered edit tool.
	editResult, err := reg.Execute(context.Background(), "file_edit",
		fmt.Sprintf(`{"file_path":%q,"old_string":"hello","new_string":"goodbye"}`, path))
	if err != nil {
		t.Fatalf("edit returned error: %v", err)
	}

	if editResult.IsError {
		t.Fatalf("edit failed: %s", toolkit.ResultText(editResult))
	}

	// Step 4: Read the file again to verify the edit.
	readResult2, err := readHandler(context.Background(), "",
		fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("second read returned error: %v", err)
	}

	if readResult2.IsError {
		t.Fatalf("second read failed: %s", toolkit.ResultText(readResult2))
	}

	if got := toolkit.ResultText(readResult2); got != "goodbye world from workflow" {
		t.Errorf("expected 'goodbye world from workflow', got %q", got)
	}
}
