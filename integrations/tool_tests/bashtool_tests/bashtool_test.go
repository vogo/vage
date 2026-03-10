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

package bashtool_tests //nolint:revive // integration test package

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/agent/taskagent"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
	"github.com/vogo/vagent/tool/bashtool"
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

// ---------- Registration Integration Tests ----------

// TestRegisterAndExecuteViaRegistry verifies the complete registration and
// execution path: Register -> List -> Get -> Execute through the tool.Registry.
func TestRegisterAndExecuteViaRegistry(t *testing.T) {
	reg := tool.NewRegistry()

	// Register bash tool with default options.
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the tool appears in List().
	defs := reg.List()
	found := false

	for _, d := range defs {
		if d.Name == "bash" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("bash tool not found in registry List()")
	}

	// Verify Get() returns the tool definition with correct fields.
	def, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash tool not found via Get()")
	}

	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify Execute() runs a command and returns the correct output.
	result, err := reg.Execute(context.Background(), "bash", `{"command":"echo integration-ok"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); strings.TrimSpace(got) != "integration-ok" {
		t.Errorf("expected 'integration-ok', got %q", got)
	}
}

// TestRegisterDuplicatePrevented verifies that registering a second bash tool
// via RegisterIfAbsent returns an error when one is already registered.
func TestRegisterDuplicatePrevented(t *testing.T) {
	reg := tool.NewRegistry()

	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	err := bashtool.Register(reg)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}

	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("expected 'already registered' in error, got: %v", err)
	}
}

// TestRegisterWithCustomOptions verifies that custom options (timeout, working
// dir, max output) are correctly applied when executing through the registry.
func TestRegisterWithCustomOptions(t *testing.T) {
	reg := tool.NewRegistry()

	if err := bashtool.Register(reg, bashtool.WithWorkingDir("/tmp")); err != nil {
		t.Fatalf("Register with options failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	// macOS resolves /tmp to /private/tmp.
	if !strings.Contains(text, "/tmp") {
		t.Errorf("expected output to contain '/tmp', got %q", text)
	}
}

// ---------- Command Execution Integration Tests ----------

// TestExecuteSuccessfulCommand verifies that a simple echo command produces
// the expected output when executed through the registry.
func TestExecuteSuccessfulCommand(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{"command":"echo hello-world"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := strings.TrimSpace(resultText(result)); got != "hello-world" {
		t.Errorf("expected 'hello-world', got %q", got)
	}
}

// TestExecuteNonZeroExitCode verifies that a command with a non-zero exit code
// returns an error result containing the exit code.
func TestExecuteNonZeroExitCode(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{"command":"exit 42"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for non-zero exit code")
	}

	text := resultText(result)
	if !strings.Contains(text, "exit code: 42") {
		t.Errorf("expected 'exit code: 42' in output, got: %s", text)
	}
}

// TestExecuteEmptyCommand verifies that an empty command string returns an
// error result with a descriptive message.
func TestExecuteEmptyCommand(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{"command":""}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for empty command")
	}

	text := resultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

// TestExecuteMalformedJSON verifies that invalid JSON arguments return an
// error result with an appropriate message.
func TestExecuteMalformedJSON(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{invalid json`)
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

// TestExecuteMissingCommandField verifies that a JSON object without a command
// field is treated as an empty command and returns an error.
func TestExecuteMissingCommandField(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for missing command field")
	}

	text := resultText(result)
	if !strings.Contains(text, "must not be empty") {
		t.Errorf("expected 'must not be empty' in output, got: %s", text)
	}
}

// TestExecuteCombinedStdoutStderr verifies that both stdout and stderr output
// is captured in the result.
func TestExecuteCombinedStdoutStderr(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	result, err := reg.Execute(context.Background(), "bash", `{"command":"echo OUT; echo ERR >&2"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	text := resultText(result)
	if !strings.Contains(text, "OUT") {
		t.Errorf("expected stdout 'OUT' in output, got: %s", text)
	}

	if !strings.Contains(text, "ERR") {
		t.Errorf("expected stderr 'ERR' in output, got: %s", text)
	}
}

// ---------- Timeout and Cancellation Integration Tests ----------

// TestExecuteTimeout verifies that a long-running command is terminated after
// the configured timeout and returns an appropriate error message.
func TestExecuteTimeout(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg, bashtool.WithTimeout(200*time.Millisecond)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	start := time.Now()
	result, err := reg.Execute(context.Background(), "bash", `{"command":"sleep 60"}`)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for timed-out command")
	}

	text := resultText(result)
	if !strings.Contains(text, "timed out") {
		t.Errorf("expected 'timed out' in output, got: %s", text)
	}

	// Verify we didn't wait the full 60 seconds.
	if elapsed > 5*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

// TestExecuteContextCancellation verifies that cancelling the parent context
// terminates the running command and returns a "cancelled" error message.
func TestExecuteContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg, bashtool.WithTimeout(30*time.Second)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	result, err := reg.Execute(ctx, "bash", `{"command":"sleep 60"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true for cancelled command")
	}

	text := resultText(result)
	if !strings.Contains(text, "cancelled") {
		t.Errorf("expected 'cancelled' in output, got: %s", text)
	}
}

// ---------- Output Truncation Integration Test ----------

// TestExecuteOutputTruncation verifies that output exceeding maxOutputBytes is
// truncated with a descriptive message indicating the truncation.
func TestExecuteOutputTruncation(t *testing.T) {
	maxBytes := 128
	reg := tool.NewRegistry()

	if err := bashtool.Register(reg, bashtool.WithMaxOutput(maxBytes)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Generate output larger than maxBytes.
	result, err := reg.Execute(context.Background(), "bash",
		`{"command":"dd if=/dev/zero bs=500 count=1 2>/dev/null | tr '\\0' 'X'"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	text := resultText(result)
	if !strings.Contains(text, "output truncated") {
		t.Errorf("expected 'output truncated' in output, got: %s", text)
	}

	if !strings.Contains(text, fmt.Sprintf("first %d bytes", maxBytes)) {
		t.Errorf("expected truncation size %d in output, got: %s", maxBytes, text)
	}
}

// ---------- Process Group Cleanup Integration Test ----------

// TestExecuteProcessGroupCleanup verifies that child processes spawned by the
// command are killed when the command times out (process group cleanup).
func TestExecuteProcessGroupCleanup(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg, bashtool.WithTimeout(500*time.Millisecond)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Spawn a child process that outlives the parent, capture its PID.
	result, err := reg.Execute(context.Background(), "bash",
		`{"command":"sh -c 'sleep 300 & echo $!; wait'"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true (timeout)")
	}

	text := resultText(result)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	if len(lines) < 1 {
		t.Fatal("expected at least one line of output with child PID")
	}

	pidStr := strings.TrimSpace(lines[0])

	pid, parseErr := strconv.Atoi(pidStr)
	if parseErr != nil {
		t.Skipf("could not parse child PID %q: %v", pidStr, parseErr)
	}

	// Give the OS a moment to clean up.
	time.Sleep(200 * time.Millisecond)

	proc, findErr := os.FindProcess(pid)
	if findErr != nil {
		return // Process already gone.
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return // Process is gone (signal returns error).
	}

	t.Errorf("child process %d is still running after process group cleanup", pid)
}

// ---------- Concurrent Execution Integration Test ----------

// TestConcurrentExecutionViaRegistry verifies that multiple commands can be
// executed concurrently through the same registry without interference.
func TestConcurrentExecutionViaRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
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

			cmd := fmt.Sprintf(`{"command":"echo concurrent-%d"}`, idx)
			results[idx], errs[idx] = reg.Execute(context.Background(), "bash", cmd)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("command %d returned error: %v", i, errs[i])

			continue
		}

		if results[i].IsError {
			t.Errorf("command %d returned IsError=true: %s", i, resultText(results[i]))

			continue
		}

		expected := fmt.Sprintf("concurrent-%d", i)
		got := strings.TrimSpace(resultText(results[i]))

		if got != expected {
			t.Errorf("command %d: expected %q, got %q", i, expected, got)
		}
	}
}

// ---------- ToolDef Schema Validation Integration Test ----------

// TestToolDefSchemaForLLMCompatibility verifies that the ToolDef schema is
// correctly structured for LLM tool calling (proper JSON Schema with required
// fields and additionalProperties constraint).
func TestToolDefSchemaForLLMCompatibility(t *testing.T) {
	bt := bashtool.New()
	def := bt.ToolDef()

	// Verify name.
	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
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

	// Verify properties contain "command".
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected 'properties' in parameters")
	}

	cmdProp, ok := props["command"].(map[string]any)
	if !ok {
		t.Fatal("expected 'command' property in parameters")
	}

	if cmdProp["type"] != "string" {
		t.Errorf("expected command type 'string', got %v", cmdProp["type"])
	}

	// Verify required field.
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected 'required' to be []string")
	}

	if len(required) != 1 || required[0] != "command" {
		t.Errorf("expected required=['command'], got %v", required)
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

	if aiTools[0].Function.Name != "bash" {
		t.Errorf("expected function name 'bash', got %q", aiTools[0].Function.Name)
	}
}

// ---------- Working Directory Integration Test ----------

// TestExecuteWithWorkingDirectory verifies that the working directory option
// is respected when executing commands through the registry.
func TestExecuteWithWorkingDirectory(t *testing.T) {
	// Create a temp directory for the test.
	tmpDir, err := os.MkdirTemp("", "bashtool-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	defer func() { _ = os.RemoveAll(tmpDir) }()

	reg := tool.NewRegistry()
	if err := bashtool.Register(reg, bashtool.WithWorkingDir(tmpDir)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Create a file in the temp dir, then verify we can list it.
	createResult, err := reg.Execute(context.Background(), "bash",
		`{"command":"touch testfile.txt && ls testfile.txt"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if createResult.IsError {
		t.Fatalf("expected success, got error: %s", resultText(createResult))
	}

	text := strings.TrimSpace(resultText(createResult))
	if text != "testfile.txt" {
		t.Errorf("expected 'testfile.txt', got %q", text)
	}

	// Verify the file was created in the temp dir.
	if _, err := os.Stat(tmpDir + "/testfile.txt"); os.IsNotExist(err) {
		t.Error("testfile.txt was not created in the working directory")
	}
}

// ---------- Error Recovery Integration Test ----------

// TestExecuteErrorThenSuccess verifies that a failed command does not affect
// subsequent successful commands through the same registry.
func TestExecuteErrorThenSuccess(t *testing.T) {
	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// First: a failing command.
	result1, err := reg.Execute(context.Background(), "bash", `{"command":"exit 1"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !result1.IsError {
		t.Fatal("expected first command to fail")
	}

	// Second: a succeeding command.
	result2, err := reg.Execute(context.Background(), "bash", `{"command":"echo recovered"}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result2.IsError {
		t.Fatalf("expected second command to succeed, got error: %s", resultText(result2))
	}

	if got := strings.TrimSpace(resultText(result2)); got != "recovered" {
		t.Errorf("expected 'recovered', got %q", got)
	}
}

// ---------- LLM Agent Integration Test ----------

// TestBashToolWithLLMAgent verifies end-to-end integration of the bash tool
// with a taskagent powered by an LLM. It creates an agent, sends a prompt
// asking it to execute a bash command, and verifies the expected output
// appears in the response. Skipped if no API key is available.
func TestBashToolWithLLMAgent(t *testing.T) {
	client, err := aimodel.NewClient(
		aimodel.WithDefaultModel(aimodel.GetEnv("OPENAI_MODEL")),
	)
	if err != nil {
		t.Skipf("Skipping LLM integration test: failed to create aimodel client: %v", err)
	}

	reg := tool.NewRegistry()
	if err := bashtool.Register(reg); err != nil {
		t.Fatalf("failed to register bash tool: %v", err)
	}

	a := taskagent.New(agent.Config{
		ID:   "bash-test-agent",
		Name: "Bash Test Agent",
	},
		taskagent.WithChatCompleter(client),
		taskagent.WithToolRegistry(reg),
		taskagent.WithSystemPrompt(prompt.StringPrompt(
			"You are a helpful assistant with access to a bash tool. Use it to execute shell commands when asked.",
		)),
		taskagent.WithMaxIterations(5),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := a.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage("Execute exactly this bash command and report the output verbatim: `echo vagent-test-ok`"),
		},
	})
	if err != nil {
		t.Fatalf("agent run failed: %v", err)
	}

	var responseText string
	for _, msg := range resp.Messages {
		if msg.Role == aimodel.RoleAssistant {
			text := msg.Content.Text()
			if text != "" {
				responseText += text
			}
		}
	}

	if !strings.Contains(responseText, "vagent-test-ok") {
		t.Errorf("expected response to contain 'vagent-test-ok', got: %s", responseText)
	}
}
