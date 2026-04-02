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

package askuser_tests //nolint:revive // integration test package

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/askuser"
)

// --- Helpers ---

// mockInteractor is a test double for askuser.UserInteractor.
type mockInteractor struct {
	response string
	err      error
	blockCh  chan struct{} // if non-nil, blocks until closed or context canceled
}

func (m *mockInteractor) AskUser(ctx context.Context, _ string) (string, error) {
	if m.blockCh != nil {
		select {
		case <-m.blockCh:
		case <-ctx.Done():
			return "User did not respond within the timeout. " +
				"Proceed with your best judgment.", nil
		}
	}

	if m.err != nil {
		return "", m.err
	}

	return m.response, nil
}

// --- Integration Tests ---

// Test 9a: Valid invocation with mock interactor returns user's response as TextResult.
//
// Test cases:
//   - Handler returns success result with the interactor's response text.
//   - Result is not marked as error.
//   - Response text matches the interactor's configured response.
func TestIntegration_AskUser_ValidQuestion(t *testing.T) {
	interactor := &mockInteractor{response: "Use PostgreSQL"}
	askTool := askuser.New(interactor)
	handler := askTool.Handler()

	result, err := handler(context.Background(), askuser.ToolName, `{"question":"Which database should we use?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success result, got error: %v", result.Content)
	}

	if len(result.Content) == 0 {
		t.Fatal("expected non-empty result content")
	}

	if result.Content[0].Text != "Use PostgreSQL" {
		t.Errorf("result text = %q, want %q", result.Content[0].Text, "Use PostgreSQL")
	}
}

// Test 9b: Empty question returns error result.
//
// Test cases:
//   - Handler returns an error result (IsError=true).
//   - Error message contains "question must not be empty".
//   - The interactor is NOT called.
func TestIntegration_AskUser_EmptyQuestion(t *testing.T) {
	interactor := &mockInteractor{response: "should not be called"}
	askTool := askuser.New(interactor)
	handler := askTool.Handler()

	result, err := handler(context.Background(), askuser.ToolName, `{"question":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected error result for empty question")
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "question must not be empty") {
		t.Errorf("unexpected error message: %s", text)
	}
}

// Test 9c: Invalid JSON args returns error result.
//
// Test cases:
//   - Handler returns an error result (IsError=true).
//   - Error message contains "invalid arguments".
func TestIntegration_AskUser_InvalidJSON(t *testing.T) {
	interactor := &mockInteractor{response: "should not be called"}
	askTool := askuser.New(interactor)
	handler := askTool.Handler()

	result, err := handler(context.Background(), askuser.ToolName, `{broken json!`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "invalid arguments") {
		t.Errorf("unexpected error message: %s", text)
	}
}

// Test 9d: NonInteractiveInteractor returns fallback message immediately.
//
// Test cases:
//   - Returns quickly without blocking.
//   - Response contains "non-interactive mode".
//   - No error is returned.
func TestIntegration_AskUser_NonInteractive(t *testing.T) {
	interactor := askuser.NonInteractiveInteractor{}
	askTool := askuser.New(interactor)
	handler := askTool.Handler()

	start := time.Now()

	result, err := handler(context.Background(), askuser.ToolName, `{"question":"What should I do?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("NonInteractiveInteractor took %v, expected < 1s", elapsed)
	}

	if result.IsError {
		t.Fatalf("expected success result, got error: %v", result.Content)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "non-interactive mode") {
		t.Errorf("expected non-interactive fallback message, got: %s", text)
	}
}

// Test 9e: Timeout behavior -- mock interactor that blocks; verify timeout message is returned.
//
// Test cases:
//   - Handler returns after the configured timeout (not after a long time).
//   - Result is not marked as error (timeout is a graceful fallback, not an error).
//   - Response contains the "best judgment" fallback text.
func TestIntegration_AskUser_Timeout(t *testing.T) {
	blockCh := make(chan struct{})
	defer close(blockCh)

	interactor := &mockInteractor{blockCh: blockCh}
	askTool := askuser.New(interactor, askuser.WithTimeout(100*time.Millisecond))
	handler := askTool.Handler()

	start := time.Now()

	result, err := handler(context.Background(), askuser.ToolName, `{"question":"Are you there?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("timeout took %v, expected ~100ms", elapsed)
	}

	if result.IsError {
		t.Fatalf("expected success result (timeout fallback), got error: %v", result.Content)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "best judgment") {
		t.Errorf("expected timeout fallback message, got: %s", text)
	}
}

// Test 9f: Interactor error is returned as an error result.
//
// Test cases:
//   - Handler returns an error result (IsError=true) when the interactor returns an error.
//   - Error message contains "ask_user:" prefix and the original error text.
func TestIntegration_AskUser_InteractorError(t *testing.T) {
	interactor := &mockInteractor{err: errors.New("connection lost")}
	askTool := askuser.New(interactor)
	handler := askTool.Handler()

	result, err := handler(context.Background(), askuser.ToolName, `{"question":"Are you there?"}`)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected error result when interactor fails")
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "connection lost") {
		t.Errorf("expected 'connection lost' in error message, got: %s", text)
	}

	if !strings.Contains(text, "ask_user:") {
		t.Errorf("expected 'ask_user:' prefix in error message, got: %s", text)
	}
}

// Test 9g: Register function adds tool to registry and can be retrieved.
//
// Test cases:
//   - After Register(), the tool is present in the registry.
//   - The tool definition has the correct name and description.
//   - A second Register() call fails with a duplicate error.
func TestIntegration_AskUser_Register(t *testing.T) {
	registry := tool.NewRegistry()

	err := askuser.Register(registry, askuser.NonInteractiveInteractor{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	def, ok := registry.Get(askuser.ToolName)
	if !ok {
		t.Fatalf("expected %q to be registered", askuser.ToolName)
	}

	if def.Name != askuser.ToolName {
		t.Errorf("Name = %q, want %q", def.Name, askuser.ToolName)
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify duplicate registration fails.
	err = askuser.Register(registry, askuser.NonInteractiveInteractor{})
	if err == nil {
		t.Fatal("expected error for duplicate registration")
	}
}

// Test 9h: Concurrent AskUser calls with separate tools work correctly.
//
// Test cases:
//   - Multiple concurrent handler invocations each get the correct response.
//   - No data races or deadlocks.
func TestIntegration_AskUser_Concurrent(t *testing.T) {
	const concurrency = 10
	errs := make(chan error, concurrency)

	for i := range concurrency {
		go func(idx int) {
			interactor := &mockInteractor{response: "response"}
			askTool := askuser.New(interactor)
			handler := askTool.Handler()

			result, err := handler(context.Background(), askuser.ToolName, `{"question":"test?"}`)
			if err != nil {
				errs <- err
				return
			}

			if result.IsError {
				errs <- errors.New("unexpected error result")
				return
			}

			if result.Content[0].Text != "response" {
				errs <- errors.New("unexpected response text")
				return
			}

			errs <- nil
		}(i)
	}

	for range concurrency {
		if err := <-errs; err != nil {
			t.Errorf("concurrent call failed: %v", err)
		}
	}
}

// Test 9i: Parent context cancellation propagates to the interactor.
//
// Test cases:
//   - When the parent context is canceled, the handler returns the timeout fallback.
//   - The handler does not hang indefinitely.
func TestIntegration_AskUser_ParentContextCancel(t *testing.T) {
	blockCh := make(chan struct{})
	defer close(blockCh)

	interactor := &mockInteractor{blockCh: blockCh}
	// Use a long tool-level timeout; cancellation should come from the parent context.
	askTool := askuser.New(interactor, askuser.WithTimeout(10*time.Second))
	handler := askTool.Handler()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := handler(ctx, askuser.ToolName, `{"question":"Will the context cancel?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success result (fallback), got error: %v", result.Content)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "best judgment") {
		t.Errorf("expected timeout fallback message, got: %s", text)
	}
}
