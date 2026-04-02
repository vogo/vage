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

package askuser

import (
	"context"
	"strings"
	"testing"
	"time"

	toolpkg "github.com/vogo/vage/tool"
)

// mockInteractor is a test double for UserInteractor.
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

func TestHandler_ValidQuestion(t *testing.T) {
	interactor := &mockInteractor{response: "Use PostgreSQL"}
	tool := New(interactor)
	handler := tool.Handler()

	result, err := handler(context.Background(), ToolName, `{"question":"Which database?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success result, got error: %v", result.Content)
	}

	if len(result.Content) == 0 || result.Content[0].Text != "Use PostgreSQL" {
		t.Errorf("unexpected result content: %v", result.Content)
	}
}

func TestHandler_EmptyQuestion(t *testing.T) {
	interactor := &mockInteractor{response: "should not be called"}
	tool := New(interactor)
	handler := tool.Handler()

	result, err := handler(context.Background(), ToolName, `{"question":""}`)
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

func TestHandler_InvalidJSON(t *testing.T) {
	interactor := &mockInteractor{response: "should not be called"}
	tool := New(interactor)
	handler := tool.Handler()

	result, err := handler(context.Background(), ToolName, `{invalid json}`)
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

func TestHandler_Timeout(t *testing.T) {
	blockCh := make(chan struct{})
	defer close(blockCh)

	interactor := &mockInteractor{blockCh: blockCh}
	tool := New(interactor, WithTimeout(50*time.Millisecond))
	handler := tool.Handler()

	result, err := handler(context.Background(), ToolName, `{"question":"Are you there?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success result (timeout fallback), got error: %v", result.Content)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "best judgment") {
		t.Errorf("expected timeout fallback message, got: %s", text)
	}
}

func TestNonInteractiveInteractor(t *testing.T) {
	interactor := NonInteractiveInteractor{}
	response, err := interactor.AskUser(context.Background(), "What is your name?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(response, "non-interactive mode") {
		t.Errorf("unexpected response: %s", response)
	}
}

func TestToolDef(t *testing.T) {
	tool := New(NonInteractiveInteractor{})
	def := tool.ToolDef()

	if def.Name != ToolName {
		t.Errorf("Name = %q, want %q", def.Name, ToolName)
	}

	if def.Source != "local" {
		t.Errorf("Source = %q, want %q", def.Source, "local")
	}

	if def.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestWithTimeout(t *testing.T) {
	tool := New(NonInteractiveInteractor{}, WithTimeout(10*time.Second))
	if tool.timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", tool.timeout)
	}
}

func TestRegister(t *testing.T) {
	registry := toolpkg.NewRegistry()
	err := Register(registry, NonInteractiveInteractor{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, ok := registry.Get(ToolName); !ok {
		t.Errorf("expected %q to be registered", ToolName)
	}
}

func TestRegister_Duplicate(t *testing.T) {
	registry := toolpkg.NewRegistry()
	err := Register(registry, NonInteractiveInteractor{})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err = Register(registry, NonInteractiveInteractor{})
	if err == nil {
		t.Fatal("expected error for duplicate registration")
	}
}
