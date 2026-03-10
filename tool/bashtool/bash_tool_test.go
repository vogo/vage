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

package bashtool

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

	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

func TestBashTool_Success(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	if got := resultText(result); got != "hello\n" {
		t.Errorf("expected %q, got %q", "hello\n", got)
	}
}

func TestBashTool_NonZeroExit(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"exit 42"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "exit code: 42") {
		t.Errorf("expected exit code 42 in output, got: %s", text)
	}
}

func TestBashTool_EmptyCommand(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":""}`)
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

func TestBashTool_Timeout(t *testing.T) {
	bt := New(WithTimeout(100 * time.Millisecond))
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"sleep 60"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "timed out") {
		t.Errorf("expected 'timed out' in output, got: %s", text)
	}
}

func TestBashTool_ContextCancel(t *testing.T) {
	bt := New(WithTimeout(30 * time.Second))
	handler := bt.Handler()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := handler(ctx, "bash", `{"command":"sleep 60"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected IsError=true")
	}

	text := resultText(result)
	if !strings.Contains(text, "cancelled") {
		t.Errorf("expected 'cancelled' in output, got: %s", text)
	}
}

func TestBashTool_WorkingDir(t *testing.T) {
	bt := New(WithWorkingDir("/tmp"))
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}

	text := resultText(result)
	if !strings.Contains(text, "/tmp") {
		t.Errorf("expected /tmp in output, got: %s", text)
	}
}

func TestBashTool_MalformedJSON(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `not json`)
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

func TestBashTool_MissingCommand(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{}`)
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

func TestBashTool_CombinedOutput(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"echo stdout; echo stderr >&2"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(result)
	if !strings.Contains(text, "stdout") {
		t.Errorf("expected stdout in output, got: %s", text)
	}

	if !strings.Contains(text, "stderr") {
		t.Errorf("expected stderr in output, got: %s", text)
	}
}

func TestBashTool_OutputTruncation(t *testing.T) {
	maxBytes := 100
	bt := New(WithMaxOutput(maxBytes))
	handler := bt.Handler()

	cmd := `{"command":"dd if=/dev/zero bs=500 count=1 2>/dev/null | tr '\\0' 'A'"}`
	result, err := handler(context.Background(), "bash", cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(result)
	if !strings.Contains(text, "output truncated") {
		t.Errorf("expected 'output truncated' in output, got: %s", text)
	}

	if !strings.Contains(text, fmt.Sprintf("first %d bytes", maxBytes)) {
		t.Errorf("expected truncation size in output, got: %s", text)
	}
}

func TestBashTool_ProcessGroupCleanup(t *testing.T) {
	bt := New(WithTimeout(500 * time.Millisecond))
	handler := bt.Handler()

	result, err := handler(context.Background(), "bash", `{"command":"sh -c 'sleep 300 & echo $!; wait'"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	time.Sleep(100 * time.Millisecond)

	proc, findErr := os.FindProcess(pid)
	if findErr != nil {
		return
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return
	}

	t.Errorf("child process %d is still running after process group cleanup", pid)
}

func TestRegisterBashTool(t *testing.T) {
	reg := tool.NewRegistry()

	if err := Register(reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash tool not found in registry")
	}

	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
	}

	if def.Source != schema.ToolSourceLocal {
		t.Errorf("expected source %q, got %q", schema.ToolSourceLocal, def.Source)
	}
}

func TestRegisterBashTool_Duplicate(t *testing.T) {
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

func TestBashTool_ConcurrentExecution(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	const n = 10

	var wg sync.WaitGroup

	errs := make([]error, n)
	results := make([]schema.ToolResult, n)

	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			cmd := fmt.Sprintf(`{"command":"echo %d"}`, idx)
			results[idx], errs[idx] = handler(context.Background(), "bash", cmd)
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("command %d returned error: %v", i, errs[i])
		}

		if results[i].IsError {
			t.Errorf("command %d returned IsError=true: %s", i, resultText(results[i]))
		}

		expected := fmt.Sprintf("%d\n", i)
		if got := resultText(results[i]); got != expected {
			t.Errorf("command %d: expected %q, got %q", i, expected, got)
		}
	}
}

func TestBashTool_ToolDefAccessor(t *testing.T) {
	bt := New()
	def := bt.ToolDef()

	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
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

	if _, ok := props["command"]; !ok {
		t.Error("expected 'command' property in parameters")
	}
}

func TestBashTool_HandlerAccessor(t *testing.T) {
	bt := New()
	handler := bt.Handler()

	if handler == nil {
		t.Fatal("expected non-nil handler")
	}

	result, err := handler(context.Background(), "bash", `{"command":"echo test"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(result))
	}
}

func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}

	return ""
}
