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

package edit_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/vogo/vage/tool/edit"
	"github.com/vogo/vage/tool/read"
	"github.com/vogo/vage/tool/toolkit"
)

// ---------- End-to-End Read Tracker Integration Tests ----------

// TestEndToEndReadThenEdit verifies the complete flow: configure a shared
// ReadTracker between read and edit tools, read a file via the read tool,
// then edit it via the edit tool. This is the primary integration scenario
// for the read-before-edit safety feature.
func TestEndToEndReadThenEdit(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	tracker := toolkit.NewMemoryReadTracker(0)

	readTool := read.New(read.WithReadTracker(tracker))
	readHandler := readTool.Handler()

	editTool := edit.New(edit.WithReadTracker(tracker))
	editHandler := editTool.Handler()

	// Step 1: Attempt edit without reading first -- should fail.
	result, err := editHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"hello","new_string":"goodbye"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true when file has not been read")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file must be read before editing") {
		t.Errorf("expected 'file must be read before editing' in output, got: %s", text)
	}

	// Verify file was not modified.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "hello world" {
		t.Errorf("file should not have been modified, got %q", string(content))
	}

	// Step 2: Read the file via read tool -- should record the read.
	readResult, err := readHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if readResult.IsError {
		t.Fatalf("read failed: %s", toolkit.ResultText(readResult))
	}

	// Step 3: Edit the file after reading -- should now succeed.
	result, err = editHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"hello","new_string":"goodbye"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success after reading, got error: %s", toolkit.ResultText(result))
	}

	// Verify file was modified.
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "goodbye world" {
		t.Errorf("expected 'goodbye world', got %q", string(content))
	}
}

// TestEndToEndReadTrackerMultipleFiles verifies that reading one file does not
// grant edit permission for a different file.
func TestEndToEndReadTrackerMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	pathA := toolkit.WriteTestFile(t, dir, "a.txt", "content A")
	pathB := toolkit.WriteTestFile(t, dir, "b.txt", "content B")

	tracker := toolkit.NewMemoryReadTracker(0)

	readTool := read.New(read.WithReadTracker(tracker))
	readHandler := readTool.Handler()

	editTool := edit.New(edit.WithReadTracker(tracker))
	editHandler := editTool.Handler()

	// Read file A only.
	readResult, err := readHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q}`, pathA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if readResult.IsError {
		t.Fatalf("read A failed: %s", toolkit.ResultText(readResult))
	}

	// Edit file A should succeed.
	result, err := editHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"content A","new_string":"modified A"}`, pathA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success editing A, got error: %s", toolkit.ResultText(result))
	}

	// Edit file B should fail (not read yet).
	result, err = editHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"content B","new_string":"modified B"}`, pathB))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for unread file B")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file must be read before editing") {
		t.Errorf("expected 'file must be read before editing' in output, got: %s", text)
	}
}

// TestEndToEndNoTrackerBackwardCompat verifies that when no ReadTracker is
// configured, edits work without reading first (backward compatibility).
func TestEndToEndNoTrackerBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "original content")

	editTool := edit.New() // no tracker
	editHandler := editTool.Handler()

	result, err := editHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"original","new_string":"modified"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success without tracker, got error: %s", toolkit.ResultText(result))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "modified content" {
		t.Errorf("expected 'modified content', got %q", string(content))
	}
}

// ---------- Deny Rules Integration Tests ----------

// TestDenyRuleBlocksEnvFile verifies that a *.env deny rule blocks editing
// .env files end-to-end via the handler.
func TestDenyRuleBlocksEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, ".env", "SECRET=value")

	et := edit.New(edit.WithDenyRules("*.env"))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"SECRET","new_string":"PUBLIC"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for denied .env file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "protected by deny rule") {
		t.Errorf("expected 'protected by deny rule' in output, got: %s", text)
	}

	if !strings.Contains(text, "*.env") {
		t.Errorf("expected deny pattern in output, got: %s", text)
	}

	// Verify file was not modified.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "SECRET=value" {
		t.Errorf("file should not have been modified, got %q", string(content))
	}
}

// TestDenyRuleBlocksLockFile verifies that a *.lock deny rule blocks editing
// lock files.
func TestDenyRuleBlocksLockFile(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "go.lock", "dependency data")

	et := edit.New(edit.WithDenyRules("*.lock"))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"dependency","new_string":"changed"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for denied lock file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "protected by deny rule") {
		t.Errorf("expected 'protected by deny rule' in output, got: %s", text)
	}
}

// TestDenyRuleBlocksCredentials verifies that an exact basename deny rule
// blocks editing credentials.json.
func TestDenyRuleBlocksCredentials(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "credentials.json", `{"key":"secret"}`)

	et := edit.New(edit.WithDenyRules("credentials.json"))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"secret","new_string":"public"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for denied credentials file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "protected by deny rule") {
		t.Errorf("expected 'protected by deny rule' in output, got: %s", text)
	}
}

// TestDenyRuleAllowsNonMatchingFile verifies that files not matching any deny
// rule are edited successfully.
func TestDenyRuleAllowsNonMatchingFile(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "main.go", "old content")

	et := edit.New(edit.WithDenyRules("*.env", "*.lock", "credentials.json"))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"old content","new_string":"new content"}`, path))
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

	if string(content) != "new content" {
		t.Errorf("expected 'new content', got %q", string(content))
	}
}

// TestDenyRuleMultiplePatterns verifies that with multiple deny rules, the
// first matching pattern is reported.
func TestDenyRuleMultiplePatterns(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "secrets.env", "KEY=value")

	et := edit.New(edit.WithDenyRules("*.lock", "*.env"))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"KEY","new_string":"CHANGED"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "*.env") {
		t.Errorf("expected matching pattern '*.env' in output, got: %s", text)
	}
}

// TestDenyRuleCombinedWithReadTracker verifies that deny rules are checked
// before the read tracker (deny rules short-circuit first).
func TestDenyRuleCombinedWithReadTracker(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, ".env", "SECRET=value")

	tracker := toolkit.NewMemoryReadTracker(0)

	// Even if the file was "read", deny rule should still block.
	cleanedPath, err := toolkit.ValidatePath("test", path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tracker.RecordRead(cleanedPath)

	et := edit.New(
		edit.WithDenyRules("*.env"),
		edit.WithReadTracker(tracker),
	)
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"SECRET","new_string":"PUBLIC"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true -- deny rule should take precedence")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "protected by deny rule") {
		t.Errorf("expected deny rule error, got: %s", text)
	}
}

// ---------- UNC Path Integration Tests ----------

// TestUNCPathBackslashRejected verifies that a UNC path with backslashes is
// rejected at the handler level.
func TestUNCPathBackslashRejected(t *testing.T) {
	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"\\\\server\\share\\file.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for UNC backslash path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "UNC paths are not allowed") {
		t.Errorf("expected 'UNC paths are not allowed' in output, got: %s", text)
	}
}

// TestUNCPathSlashRejected verifies that a UNC path with forward slashes is
// rejected at the handler level.
func TestUNCPathSlashRejected(t *testing.T) {
	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", `{"file_path":"//server/share/file.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for UNC slash path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "UNC paths are not allowed") {
		t.Errorf("expected 'UNC paths are not allowed' in output, got: %s", text)
	}
}

// ---------- File Size Limits Integration Tests ----------

// TestFileSizeLimitEnforced verifies that files exceeding the configured
// maxFileBytes are rejected with a descriptive error including both actual
// and maximum sizes.
func TestFileSizeLimitEnforced(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("X", 500)
	path := toolkit.WriteTestFile(t, dir, "large.txt", content)

	et := edit.New(edit.WithMaxFileBytes(200))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"X","new_string":"Y","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for file exceeding max size")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file size") {
		t.Errorf("expected 'file size' in output, got: %s", text)
	}

	if !strings.Contains(text, "500 bytes") {
		t.Errorf("expected actual file size '500 bytes' in output, got: %s", text)
	}

	if !strings.Contains(text, "200 bytes") {
		t.Errorf("expected max size '200 bytes' in output, got: %s", text)
	}

	if !strings.Contains(text, "exceeds maximum allowed") {
		t.Errorf("expected 'exceeds maximum allowed' in output, got: %s", text)
	}
}

// TestFileSizeLimitAllowsSmallFile verifies that a file within the size limit
// is edited successfully.
func TestFileSizeLimitAllowsSmallFile(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("X", 50)
	path := toolkit.WriteTestFile(t, dir, "small.txt", content)

	et := edit.New(edit.WithMaxFileBytes(200))
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"X","new_string":"Y","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}
}

// ---------- Read-Only File Integration Tests ----------

// TestReadOnlyFileRejected verifies that attempting to edit a read-only file
// returns a descriptive error including the file mode.
func TestReadOnlyFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "readonly.txt", "content")

	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}

	t.Cleanup(func() {
		_ = os.Chmod(path, 0o644)
	})

	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"content","new_string":"changed"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for read-only file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file appears to be read-only") {
		t.Errorf("expected 'file appears to be read-only' in output, got: %s", text)
	}

	if !strings.Contains(text, "r--") {
		t.Errorf("expected file mode with 'r--' in output, got: %s", text)
	}

	// Verify file was not modified.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "content" {
		t.Errorf("file should not have been modified, got %q", string(content))
	}
}

// ---------- Replace All Integration Tests ----------

// TestReplaceAllWithDenyRulesAndTracker verifies that replace_all works
// correctly when combined with other safety features (deny rules that pass,
// read tracker that was satisfied).
func TestReplaceAllWithDenyRulesAndTracker(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "code.go", "foo bar foo baz foo")

	tracker := toolkit.NewMemoryReadTracker(0)

	readTool := read.New(read.WithReadTracker(tracker))
	readHandler := readTool.Handler()

	// Read first.
	readResult, err := readHandler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if readResult.IsError {
		t.Fatalf("read failed: %s", toolkit.ResultText(readResult))
	}

	// Edit with replace_all, deny rules that don't match, and tracker satisfied.
	editTool := edit.New(
		edit.WithDenyRules("*.env", "*.lock"),
		edit.WithReadTracker(tracker),
	)
	editHandler := editTool.Handler()

	result, err := editHandler(context.Background(), "", fmt.Sprintf(
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

// ---------- Error Scenarios Integration Tests ----------

// TestNotFoundErrorGuidance verifies that the old_string-not-found error
// includes actionable guidance (whitespace/indentation mismatch hint).
func TestNotFoundErrorGuidance(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "hello world")

	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"missing text","new_string":"replacement"}`, path))
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

	if !strings.Contains(text, "whitespace/indentation mismatch") {
		t.Errorf("expected actionable guidance in output, got: %s", text)
	}

	if !strings.Contains(text, "changed since last read") {
		t.Errorf("expected 'changed since last read' guidance in output, got: %s", text)
	}
}

// TestNonUniqueMatchWithoutReplaceAll verifies that multiple matches without
// replace_all returns a clear error with match count and guidance.
func TestNonUniqueMatchWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := toolkit.WriteTestFile(t, dir, "test.txt", "abc def abc ghi abc")

	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"abc","new_string":"xyz"}`, path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for non-unique match")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "matches 3 locations") {
		t.Errorf("expected 'matches 3 locations' in output, got: %s", text)
	}

	if !strings.Contains(text, "replace_all") {
		t.Errorf("expected 'replace_all' guidance in output, got: %s", text)
	}

	// Verify file was not modified.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "abc def abc ghi abc" {
		t.Errorf("file should not have been modified, got %q", string(content))
	}
}

// TestFileNotFoundError verifies that editing a non-existent file returns a
// clear error.
func TestFileNotFoundError(t *testing.T) {
	et := edit.New()
	handler := et.Handler()

	result, err := handler(context.Background(), "",
		`{"file_path":"/tmp/nonexistent_safety_integration_test_42.txt","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for non-existent file")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "file does not exist") {
		t.Errorf("expected 'file does not exist' in output, got: %s", text)
	}
}

// ---------- Combined Safety Pipeline Integration Test ----------

// TestSafetyPipelineOrder verifies the correct ordering of safety checks:
// path validation -> deny rules -> read tracker -> file stat checks.
// Each check should short-circuit before reaching the next stage.
func TestSafetyPipelineOrder(t *testing.T) {
	dir := t.TempDir()

	tracker := toolkit.NewMemoryReadTracker(0)

	et := edit.New(
		edit.WithDenyRules("*.env"),
		edit.WithReadTracker(tracker),
	)
	handler := et.Handler()

	// 1. Invalid path check comes first.
	result, err := handler(context.Background(), "",
		`{"file_path":"","old_string":"a","new_string":"b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty path")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected path validation error, got: %s", text)
	}

	// 2. Deny rule check comes before read tracker.
	envPath := toolkit.WriteTestFile(t, dir, "test.env", "SECRET=value")

	result, err = handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"SECRET","new_string":"PUBLIC"}`, envPath))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for denied file")
	}

	text = toolkit.ResultText(result)
	if !strings.Contains(text, "protected by deny rule") {
		t.Errorf("expected deny rule error (not read tracker error), got: %s", text)
	}

	// 3. Read tracker check comes before file stat.
	goPath := toolkit.WriteTestFile(t, dir, "main.go", "content")

	result, err = handler(context.Background(), "", fmt.Sprintf(
		`{"file_path":%q,"old_string":"content","new_string":"changed"}`, goPath))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for unread file")
	}

	text = toolkit.ResultText(result)
	if !strings.Contains(text, "file must be read before editing") {
		t.Errorf("expected read tracker error, got: %s", text)
	}
}
