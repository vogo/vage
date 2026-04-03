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

package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

// mockRegistry is a minimal mock for testing TruncatingToolRegistry.
type mockRegistry struct {
	executeFn   func(ctx context.Context, name, args string) (schema.ToolResult, error)
	registered  []schema.ToolDef
	merged      []schema.ToolDef
	unregName   string
	registerErr error
}

func (m *mockRegistry) Register(def schema.ToolDef, _ ToolHandler) error {
	if m.registerErr != nil {
		return m.registerErr
	}
	m.registered = append(m.registered, def)
	return nil
}

func (m *mockRegistry) Unregister(name string) error {
	m.unregName = name
	return nil
}

func (m *mockRegistry) Get(name string) (schema.ToolDef, bool) {
	for _, d := range m.registered {
		if d.Name == name {
			return d, true
		}
	}
	return schema.ToolDef{}, false
}

func (m *mockRegistry) List() []schema.ToolDef {
	return m.registered
}

func (m *mockRegistry) Merge(defs []schema.ToolDef) {
	m.merged = append(m.merged, defs...)
}

func (m *mockRegistry) Execute(ctx context.Context, name, args string) (schema.ToolResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, name, args)
	}
	return schema.ToolResult{}, nil
}

func TestTruncatingToolRegistry_NoTruncationBelowLimit(t *testing.T) {
	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("tc1", "short result"), nil
		},
	}

	tr := NewTruncatingToolRegistry(inner, 2000)
	result, err := tr.Execute(context.Background(), "test", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Content[0].Text != "short result" {
		t.Errorf("result should not be modified, got %q", result.Content[0].Text)
	}
}

func TestTruncatingToolRegistry_TruncatesAboveLimit(t *testing.T) {
	// 50,000 characters = ~12,500 tokens
	largeOutput := strings.Repeat("x", 50000)

	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("tc1", largeOutput), nil
		},
	}

	tr := NewTruncatingToolRegistry(inner, 2000)
	result, err := tr.Execute(context.Background(), "test", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].Text
	if len(text) >= len(largeOutput) {
		t.Error("result should be truncated")
	}

	if !strings.Contains(text, "[truncated:") {
		t.Error("result should contain truncation marker")
	}

	if !strings.Contains(text, "showing first 2000 of 12500 estimated tokens") {
		t.Errorf("truncation marker should show correct token counts, got %q", text)
	}
}

func TestTruncatingToolRegistry_ErrorPassthrough(t *testing.T) {
	expectedErr := errors.New("tool error")
	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{}, expectedErr
		},
	}

	tr := NewTruncatingToolRegistry(inner, 2000)
	_, err := tr.Execute(context.Background(), "test", "{}")
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error passthrough, got %v", err)
	}
}

func TestTruncatingToolRegistry_ErrorResultNotTruncated(t *testing.T) {
	largeOutput := strings.Repeat("x", 50000)
	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{
				ToolCallID: "tc1",
				Content:    []schema.ContentPart{{Type: "text", Text: largeOutput}},
				IsError:    true,
			}, nil
		},
	}

	tr := NewTruncatingToolRegistry(inner, 2000)
	result, err := tr.Execute(context.Background(), "test", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Content[0].Text != largeOutput {
		t.Error("error results should not be truncated")
	}
}

func TestTruncatingToolRegistry_ZeroMaxTokensDisables(t *testing.T) {
	largeOutput := strings.Repeat("x", 50000)
	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("tc1", largeOutput), nil
		},
	}

	tr := NewTruncatingToolRegistry(inner, 0)
	result, err := tr.Execute(context.Background(), "test", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Content[0].Text != largeOutput {
		t.Error("zero maxTokens should disable truncation")
	}
}

func TestTruncatingToolRegistry_NegativeMaxTokensDisables(t *testing.T) {
	largeOutput := strings.Repeat("x", 50000)
	inner := &mockRegistry{
		executeFn: func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("tc1", largeOutput), nil
		},
	}

	tr := NewTruncatingToolRegistry(inner, -1)
	result, err := tr.Execute(context.Background(), "test", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Content[0].Text != largeOutput {
		t.Error("negative maxTokens should disable truncation")
	}
}

func TestTruncatingToolRegistry_DelegatedMethods(t *testing.T) {
	inner := &mockRegistry{}

	tr := NewTruncatingToolRegistry(inner, 2000)

	// Register
	def := schema.ToolDef{Name: "test-tool", Description: "a test tool"}
	if err := tr.Register(def, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(inner.registered) != 1 || inner.registered[0].Name != "test-tool" {
		t.Error("Register should delegate to inner")
	}

	// Get
	got, ok := tr.Get("test-tool")
	if !ok || got.Name != "test-tool" {
		t.Error("Get should delegate to inner")
	}

	// List
	list := tr.List()
	if len(list) != 1 || list[0].Name != "test-tool" {
		t.Error("List should delegate to inner")
	}

	// Merge
	tr.Merge([]schema.ToolDef{{Name: "merged-tool"}})
	if len(inner.merged) != 1 || inner.merged[0].Name != "merged-tool" {
		t.Error("Merge should delegate to inner")
	}

	// Unregister
	if err := tr.Unregister("test-tool"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if inner.unregName != "test-tool" {
		t.Error("Unregister should delegate to inner")
	}
}

func TestTruncatingToolRegistry_InterfaceCompliance(t *testing.T) {
	// This is a compile-time check via the var _ line in truncate.go,
	// but including it here documents the intent.
	var _ ToolRegistry = (*TruncatingToolRegistry)(nil)
}
