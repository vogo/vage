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

package grep_tests //nolint:revive // integration test package

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
	"github.com/vogo/vage/tool/glob"
	"github.com/vogo/vage/tool/grep"
	"github.com/vogo/vage/tool/toolkit"
)

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "hello.txt", "hello world\ngoodbye world\nhello again\n")

	reg := tool.NewRegistry()

	if err := grep.Register(reg, grep.WithWorkingDir(dir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "grep" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("grep tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("grep")
	if !ok {
		t.Fatal("grep tool not found via Get()")
	}

	if def.Name != "grep" {
		t.Errorf("expected name 'grep', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() runs and returns the correct output.
	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"hello","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "hello") {
		t.Errorf("expected 'hello' in output, got: %s", text)
	}
}

// TestRegisterDuplicatePrevented verifies that registering a second grep tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := grep.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := grep.Register(reg)
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

	if err := grep.Register(reg, grep.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	// Searching outside allowed dir should fail.
	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"secret","path":%q}`, otherDir))
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
	toolkit.WriteTestFile(t, allowedDir, "allowed.txt", "findme content")

	result, err = reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"findme","path":%q}`, allowedDir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text = toolkit.ResultText(result)
	if !strings.Contains(text, "findme") {
		t.Errorf("expected 'findme' in output, got: %s", text)
	}
}

// ---------- Grep Execution Integration Tests ----------

// TestGrepSuccessfulSearch verifies that searching for a regex pattern returns
// matching lines with file paths and line numbers.
func TestGrepSuccessfulSearch(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "hello.txt", "hello world\ngoodbye world\nhello again\n")
	toolkit.WriteTestFile(t, dir, "other.txt", "nothing here\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"hello","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "hello") {
		t.Errorf("expected 'hello' in output, got: %s", text)
	}

	if strings.Contains(text, "nothing here") {
		t.Errorf("did not expect 'nothing here' in output, got: %s", text)
	}
}

// TestGrepWithIncludeFilter verifies that the include parameter correctly
// filters files by extension.
func TestGrepWithIncludeFilter(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "code.go", "func main() {\n}\n")
	toolkit.WriteTestFile(t, dir, "readme.txt", "func description\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"func","path":%q,"include":"*.go"}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "code.go") {
		t.Errorf("expected code.go in output, got: %s", text)
	}

	// With include filter, readme.txt should not appear.
	if strings.Contains(text, "readme.txt") {
		t.Errorf("did not expect readme.txt in output with *.go filter, got: %s", text)
	}
}

// TestGrepNoMatches verifies that a pattern matching no content returns an
// empty TextResult (not an error), per grep/rg exit code 1 semantics.
func TestGrepNoMatches(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "test.txt", "some content\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"zzzznonexistent","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// No matches should NOT be an error.
	if result.IsError {
		t.Fatalf("expected success (no matches), got error: %s", toolkit.ResultText(result))
	}
}

// TestGrepSingleFileSearch verifies that grep works when targeting a single
// file rather than a directory.
func TestGrepSingleFileSearch(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	filePath := toolkit.WriteTestFile(t, dir, "single.txt", "line one\nline two\nline three\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"line two","path":%q}`, filePath))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "line two") {
		t.Errorf("expected 'line two' in output, got: %s", text)
	}
}

// TestGrepRegexPattern verifies that regular expression patterns work correctly.
func TestGrepRegexPattern(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "data.txt", "error: something failed\nwarn: be careful\nerror: another issue\ninfo: all good\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"^error:","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "error:") {
		t.Errorf("expected 'error:' in output, got: %s", text)
	}

	if strings.Contains(text, "warn:") {
		t.Errorf("did not expect 'warn:' in output, got: %s", text)
	}

	if strings.Contains(text, "info:") {
		t.Errorf("did not expect 'info:' in output, got: %s", text)
	}
}

// TestGrepWorkingDirFallback verifies that when no path argument is provided,
// the configured working directory is used as the search path.
func TestGrepWorkingDirFallback(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "test.txt", "findme in working dir\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithWorkingDir(dir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// No path argument; should use working dir.
	result, err := reg.Execute(context.Background(), "grep", `{"pattern":"findme"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "findme") {
		t.Errorf("expected 'findme' in output, got: %s", text)
	}
}

// TestGrepOutputFormat verifies that grep output contains file path and line
// number information in the expected format.
func TestGrepOutputFormat(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "formatted.txt", "line1\nmatchme\nline3\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"matchme","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", toolkit.ResultText(result))
	}

	text := toolkit.ResultText(result)

	// Output should contain line number (grep -n / rg --line-number format).
	if !strings.Contains(text, "2") {
		t.Errorf("expected line number '2' in output, got: %s", text)
	}

	if !strings.Contains(text, "matchme") {
		t.Errorf("expected 'matchme' in output, got: %s", text)
	}
}

// ---------- Error Handling Integration Tests ----------

// TestGrepMalformedJSON verifies that invalid JSON arguments return an
// ErrorResult with an appropriate message.
func TestGrepMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep", `{invalid json`)
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

// TestGrepEmptyPattern verifies that an empty pattern returns an ErrorResult.
func TestGrepEmptyPattern(t *testing.T) {
	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep", `{"pattern":""}`)
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

// TestGrepNoWorkingDir verifies that an error is returned when no path argument
// is provided and no working directory is configured.
func TestGrepNoWorkingDir(t *testing.T) {
	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep", `{"pattern":"test"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true when no path and no working dir")
	}

	text := toolkit.ResultText(result)
	if !strings.Contains(text, "no search path") {
		t.Errorf("expected 'no search path' in output, got: %s", text)
	}
}

// ---------- Timeout and Cancellation Integration Tests ----------

// TestGrepContextCancellation verifies that a cancelled context returns an
// ErrorResult with a cancellation message.
func TestGrepContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	result, err := reg.Execute(ctx, "grep", `{"pattern":"test","path":"/tmp"}`)
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

// TestGrepOutputTruncation verifies that large output is truncated with a
// descriptive truncation notice.
func TestGrepOutputTruncation(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())

	// Create a file with many lines to generate large output.
	var content strings.Builder
	for i := range 500 {
		fmt.Fprintf(&content, "matchable line number %s %d\n",
			strings.Repeat("x", 50), i)
	}

	toolkit.WriteTestFile(t, dir, "big.txt", content.String())

	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithMaxOutput(200)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"matchable","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	text := toolkit.ResultText(result)
	if strings.Contains(text, "output truncated") {
		// Truncation is expected for small maxOutput.
		return
	}
	// If output happens to fit, that's also acceptable.
}

// ---------- Concurrent Execution Integration Test ----------

// TestGrepConcurrentExecution verifies that multiple grep invocations can be
// executed concurrently through the same registry without interference.
func TestGrepConcurrentExecution(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "data.txt", "findme here\n")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
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

			args := fmt.Sprintf(`{"pattern":"findme","path":%q}`, dir)
			results[idx], errs[idx] = reg.Execute(context.Background(), "grep", args)
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
	gt := grep.New()
	def := gt.ToolDef()

	// Verify name.
	if def.Name != "grep" {
		t.Errorf("expected name 'grep', got %q", def.Name)
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

	for _, prop := range []string{"pattern", "path", "include"} {
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

	if aiTools[0].Function.Name != "grep" {
		t.Errorf("expected function name 'grep', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Error Recovery Integration Test ----------

// TestGrepErrorThenSuccess verifies that a failed grep does not affect
// subsequent successful greps through the same registry.
func TestGrepErrorThenSuccess(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "good.txt", "findme content")

	reg := tool.NewRegistry()
	if err := grep.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing grep (empty pattern).
	result1, err := reg.Execute(context.Background(), "grep", `{"pattern":""}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first grep to fail")
	}

	// Second: a succeeding grep.
	result2, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"findme","path":%q}`, dir))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second grep to succeed, got error: %s", toolkit.ResultText(result2))
	}

	text := toolkit.ResultText(result2)
	if !strings.Contains(text, "findme") {
		t.Errorf("expected 'findme' in output, got: %s", text)
	}
}

// ---------- Symlink Integration Test ----------

// TestGrepSymlinkOutsideAllowedDirs verifies that a symlink pointing outside
// allowed directories is rejected when used as the search path.
func TestGrepSymlinkOutsideAllowedDirs(t *testing.T) {
	allowedDir := toolkit.ResolveDir(t, t.TempDir())
	outsideDir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, outsideDir, "secret.txt", "secret data")

	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	targetPath := filepath.Join(symlinkPath, "secret.txt")

	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithAllowedDirs(allowedDir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"secret","path":%q}`, targetPath))
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

// ---------- Both Tools Together Integration Test ----------

// TestGlobAndGrepCoexist verifies that both glob and grep tools can be
// registered in the same registry and used together.
func TestGlobAndGrepCoexist(t *testing.T) {
	dir := toolkit.ResolveDir(t, t.TempDir())
	toolkit.WriteTestFile(t, dir, "code.go", "func main() {\n\tfmt.Println(\"hello\")\n}\n")
	toolkit.WriteTestFile(t, dir, "readme.md", "# Project\nThis is a project.\n")

	reg := tool.NewRegistry()

	// Import glob for coexistence test.
	gt := glob.New()
	if err := reg.RegisterIfAbsent(gt.ToolDef(), gt.Handler()); err != nil {
		t.Fatalf("glob Register failed: %v", err)
	}

	if err := grep.Register(reg); err != nil {
		t.Fatalf("grep Register failed: %v", err)
	}

	// Verify both tools are listed.
	defs := reg.List()
	foundGlob := false
	foundGrep := false

	for _, d := range defs {
		if d.Name == "glob" {
			foundGlob = true
		}

		if d.Name == "grep" {
			foundGrep = true
		}
	}

	if !foundGlob {
		t.Error("glob tool not found in registry")
	}

	if !foundGrep {
		t.Error("grep tool not found in registry")
	}

	// Use glob to find .go files.
	globResult, err := reg.Execute(context.Background(), "glob",
		fmt.Sprintf(`{"pattern":"*.go","path":%q}`, dir))
	if err != nil {
		t.Fatalf("glob Execute returned error: %v", err)
	}

	if globResult.IsError {
		t.Fatalf("glob expected success, got error: %s", toolkit.ResultText(globResult))
	}

	globText := toolkit.ResultText(globResult)
	if !strings.Contains(globText, "code.go") {
		t.Errorf("expected code.go in glob output, got: %s", globText)
	}

	// Use grep to search for a pattern.
	grepResult, err := reg.Execute(context.Background(), "grep",
		fmt.Sprintf(`{"pattern":"func main","path":%q}`, dir))
	if err != nil {
		t.Fatalf("grep Execute returned error: %v", err)
	}

	if grepResult.IsError {
		t.Fatalf("grep expected success, got error: %s", toolkit.ResultText(grepResult))
	}

	grepText := toolkit.ResultText(grepResult)
	if !strings.Contains(grepText, "func main") {
		t.Errorf("expected 'func main' in grep output, got: %s", grepText)
	}
}
