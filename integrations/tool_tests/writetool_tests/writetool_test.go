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

package writetool_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
	"github.com/vogo/vagent/tool/toolkit"
	"github.com/vogo/vagent/tool/writetool"
)

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	reg := tool.NewRegistry()

	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "file_write" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("file_write tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("file_write")
	if !ok {
		t.Fatal("file_write tool not found via Get()")
	}

	if def.Name != "file_write" {
		t.Errorf("expected name 'file_write', got %q", def.Name)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() creates the file with correct content.
	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"hello world"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
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

// TestRegisterDuplicatePrevented verifies that registering a second write tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := writetool.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := writetool.Register(reg)
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
	forbiddenPath := filepath.Join(otherDir, "forbidden.txt")

	reg := tool.NewRegistry()

	if err := writetool.Register(reg, writetool.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	// Writing to outside allowed dir should fail.
	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"data"}`, forbiddenPath))
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

	// Writing to inside allowed dir should succeed.
	allowedPath := filepath.Join(allowedDir, "allowed.txt")

	result, err = reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"allowed data"}`, allowedPath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}
}

// ---------- Write Execution Integration Tests ----------

// TestWriteCreateNewFile verifies that writing to a non-existent file creates
// it with the correct content.
func TestWriteCreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brand-new.txt")

	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"brand new content"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "brand new content" {
		t.Errorf("expected %q, got %q", "brand new content", string(content))
	}
}

// TestWriteOverwriteExistingFile verifies that writing to an existing file
// replaces its content.
func TestWriteOverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")

	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"new content"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "new content" {
		t.Errorf("expected %q, got %q", "new content", string(content))
	}
}

// TestWriteCreateParentDirectories verifies that parent directories are
// created automatically when writing to a deeply nested path.
func TestWriteCreateParentDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"deep content"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "deep content" {
		t.Errorf("expected %q, got %q", "deep content", string(content))
	}
}

// TestWriteEmptyContent verifies that writing empty content creates an empty file.
func TestWriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":""}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(content) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(content))
	}
}

// ---------- Error Handling Integration Tests ----------

// TestWriteEmptyPath verifies that an empty file_path returns an ErrorResult.
func TestWriteEmptyPath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		`{"file_path":"","content":"data"}`)
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

// TestWriteRelativePath verifies that a relative path returns an ErrorResult.
func TestWriteRelativePath(t *testing.T) {
	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		`{"file_path":"relative/path.txt","content":"data"}`)
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

// TestWriteMalformedJSON verifies that invalid JSON arguments return an ErrorResult.
func TestWriteMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write", `{invalid json`)
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

// TestWriteExceedsMaxWriteBytes verifies that content exceeding the max write
// size returns an ErrorResult.
func TestWriteExceedsMaxWriteBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	reg := tool.NewRegistry()
	if err := writetool.Register(reg, writetool.WithMaxWriteBytes(10)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"this content is way too long for the limit"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for content exceeding max")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' in output, got: %s", text)
	}
}

// TestWriteContextCancellation verifies that a cancelled context returns an
// ErrorResult with a descriptive message.
func TestWriteContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := reg.Execute(ctx, "file_write",
		`{"file_path":"/tmp/whatever.txt","content":"data"}`)
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
	wt := writetool.New()
	def := wt.ToolDef()

	// Verify name.
	if def.Name != "file_write" {
		t.Errorf("expected name 'file_write', got %q", def.Name)
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

	for _, prop := range []string{"file_path", "content", "create_only"} {
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

	if !requiredSet["file_path"] || !requiredSet["content"] {
		t.Errorf("expected required to contain 'file_path' and 'content', got %v", required)
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

	if aiTools[0].Function.Name != "file_write" {
		t.Errorf("expected function name 'file_write', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Error Recovery Integration Test ----------

// TestWriteErrorThenSuccess verifies that a failed write does not affect
// subsequent successful writes through the same registry.
func TestWriteErrorThenSuccess(t *testing.T) {
	dir := t.TempDir()

	reg := tool.NewRegistry()
	if err := writetool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing write (empty path).
	result1, err := reg.Execute(context.Background(), "file_write",
		`{"file_path":"","content":"data"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first write to fail")
	}

	// Second: a succeeding write.
	path := filepath.Join(dir, "recovered.txt")

	result2, err := reg.Execute(context.Background(), "file_write",
		fmt.Sprintf(`{"file_path":%q,"content":"recovered"}`, path))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second write to succeed, got error: %s", toolkit.ResultText(result2))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if string(content) != "recovered" {
		t.Errorf("expected 'recovered', got %q", string(content))
	}
}
