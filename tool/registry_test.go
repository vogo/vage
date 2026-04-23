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
	"testing"

	"github.com/vogo/vage/schema"
)

func echoHandler(_ context.Context, name, args string) (schema.ToolResult, error) {
	return schema.TextResult("", name+":"+args), nil
}

func errorHandler(_ context.Context, _, _ string) (schema.ToolResult, error) {
	return schema.ToolResult{}, errors.New("tool failed")
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	def := schema.ToolDef{Name: "echo", Description: "echo tool"}
	if err := r.Register(def, echoHandler); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	got, ok := r.Get("echo")
	if !ok {
		t.Fatal("Get returned false for registered tool")
	}
	if got.Name != "echo" {
		t.Errorf("Name = %q, want %q", got.Name, "echo")
	}
	if got.Description != "echo tool" {
		t.Errorf("Description = %q, want %q", got.Description, "echo tool")
	}
}

func TestRegistry_GetNotFound(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("missing")
	if ok {
		t.Error("Get returned true for unregistered tool")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	def := schema.ToolDef{Name: "tmp", Description: "temporary"}
	_ = r.Register(def, echoHandler)
	_ = r.Unregister("tmp")
	_, ok := r.Get("tmp")
	if ok {
		t.Error("Get returned true after Unregister")
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(schema.ToolDef{Name: "a", Description: "tool a"}, echoHandler)
	_ = r.Register(schema.ToolDef{Name: "b", Description: "tool b"}, echoHandler)
	defs := r.List()
	if len(defs) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("List returned %v, want a and b", defs)
	}
}

// TestRegistry_List_DeterministicOrder guards the name-sorted iteration
// contract relied on by the Anthropic prompt-cache prefix — map-range
// order would otherwise shuffle the tools block across calls and bust
// the cache key on every request.
func TestRegistry_List_DeterministicOrder(t *testing.T) {
	// Register in a non-alphabetical order to prove the registry sorts.
	r := NewRegistry()
	_ = r.Register(schema.ToolDef{Name: "delta"}, echoHandler)
	_ = r.Register(schema.ToolDef{Name: "alpha"}, echoHandler)
	_ = r.Register(schema.ToolDef{Name: "charlie"}, echoHandler)
	_ = r.Register(schema.ToolDef{Name: "bravo"}, echoHandler)

	want := []string{"alpha", "bravo", "charlie", "delta"}
	for i := 0; i < 20; i++ {
		defs := r.List()
		if len(defs) != len(want) {
			t.Fatalf("iter %d len(List) = %d, want %d", i, len(defs), len(want))
		}
		for j, d := range defs {
			if d.Name != want[j] {
				t.Errorf("iter %d defs[%d].Name = %q, want %q", i, j, d.Name, want[j])
			}
		}
	}
}

func TestRegistry_Execute(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(schema.ToolDef{Name: "echo"}, echoHandler)

	result, err := r.Execute(context.Background(), "echo", `{"msg":"hi"}`)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	text := result.Content[0].Text
	want := `echo:{"msg":"hi"}`
	if text != want {
		t.Errorf("Execute result = %q, want %q", text, want)
	}
}

func TestRegistry_Execute_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "missing", "")
	if err == nil {
		t.Fatal("expected error for missing tool")
	}
}

func TestRegistry_Execute_HandlerError(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(schema.ToolDef{Name: "fail"}, errorHandler)

	_, err := r.Execute(context.Background(), "fail", "")
	if err == nil {
		t.Fatal("expected error from handler")
	}
	if err.Error() != "tool failed" {
		t.Errorf("error = %q, want %q", err.Error(), "tool failed")
	}
}

func TestRegistry_Merge(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(schema.ToolDef{Name: "local", Description: "local tool"}, echoHandler)

	r.Merge([]schema.ToolDef{
		{Name: "remote", Description: "remote tool"},
		{Name: "local", Description: "should not overwrite"},
	})

	defs := r.List()
	if len(defs) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(defs))
	}

	// Verify local tool was not overwritten.
	got, _ := r.Get("local")
	if got.Description != "local tool" {
		t.Errorf("local Description = %q, want %q (should not be overwritten)", got.Description, "local tool")
	}

	// Merged tool has no handler.
	_, err := r.Execute(context.Background(), "remote", "")
	if err == nil {
		t.Fatal("expected error executing merged tool with no handler")
	}
}

func TestToAIModelTools(t *testing.T) {
	defs := []schema.ToolDef{
		{Name: "get_weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}},
		{Name: "search", Description: "Search the web"},
	}
	tools := ToAIModelTools(defs)
	if len(tools) != 2 {
		t.Fatalf("len = %d, want 2", len(tools))
	}
	if tools[0].Type != "function" {
		t.Errorf("[0].Type = %q, want %q", tools[0].Type, "function")
	}
	if tools[0].Function.Name != "get_weather" {
		t.Errorf("[0].Function.Name = %q, want %q", tools[0].Function.Name, "get_weather")
	}
	if tools[0].Function.Description != "Get weather" {
		t.Errorf("[0].Function.Description = %q, want %q", tools[0].Function.Description, "Get weather")
	}
	if tools[1].Function.Name != "search" {
		t.Errorf("[1].Function.Name = %q, want %q", tools[1].Function.Name, "search")
	}
}

func TestFilterTools(t *testing.T) {
	defs := []schema.ToolDef{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}

	// Empty filter returns all.
	all := FilterTools(defs, nil)
	if len(all) != 3 {
		t.Errorf("FilterTools(nil) len = %d, want 3", len(all))
	}

	// Filter by whitelist.
	filtered := FilterTools(defs, []string{"a", "c"})
	if len(filtered) != 2 {
		t.Fatalf("FilterTools len = %d, want 2", len(filtered))
	}
	names := map[string]bool{}
	for _, d := range filtered {
		names[d.Name] = true
	}
	if !names["a"] || !names["c"] {
		t.Errorf("filtered = %v, want a and c", filtered)
	}
	if names["b"] {
		t.Error("filtered should not contain b")
	}
}

func TestFilterTools_NoMatch(t *testing.T) {
	defs := []schema.ToolDef{{Name: "a"}}
	filtered := FilterTools(defs, []string{"x"})
	if len(filtered) != 0 {
		t.Errorf("FilterTools len = %d, want 0", len(filtered))
	}
}
