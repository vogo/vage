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
	"fmt"
	"slices"
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
	for i := range 20 {
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

// recordingExternalCaller records CallTool invocations for middleware tests.
type recordingExternalCaller struct {
	calls  *int
	result schema.ToolResult
	err    error
	name   *string
	args   *string
}

func (c *recordingExternalCaller) CallTool(_ context.Context, name, args string) (schema.ToolResult, error) {
	if c.calls != nil {
		*c.calls++
	}
	if c.name != nil {
		*c.name = name
	}
	if c.args != nil {
		*c.args = args
	}
	return c.result, c.err
}

// tracingExecuteMiddleware appends "<name>:before" before next and
// "<name>:after" after it returns, so tests can assert onion order.
func tracingExecuteMiddleware(trace *[]string, name string) ExecuteMiddleware {
	return func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, toolName, args string) (schema.ToolResult, error) {
			*trace = append(*trace, name+":before")
			result, err := next(ctx, toolName, args)
			*trace = append(*trace, name+":after")
			return result, err
		}
	}
}

func TestRegistry_Execute_NoMiddleware_ExternalSuccess(t *testing.T) {
	calls := 0
	ext := &recordingExternalCaller{
		calls:  &calls,
		result: schema.TextResult("", "external-ok"),
	}
	r := NewRegistry(WithExternalCaller(ext))
	r.Merge([]schema.ToolDef{{Name: "remote"}})

	result, err := r.Execute(context.Background(), "remote", `{"q":1}`)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Text() != "external-ok" {
		t.Errorf("result = %q, want %q", result.Text(), "external-ok")
	}
	if calls != 1 {
		t.Errorf("external calls = %d, want 1", calls)
	}
}

func TestRegistry_Execute_NoMiddleware_ExternalError(t *testing.T) {
	ext := &recordingExternalCaller{err: errors.New("remote failed")}
	r := NewRegistry(WithExternalCaller(ext))
	r.Merge([]schema.ToolDef{{Name: "remote"}})

	_, err := r.Execute(context.Background(), "remote", "")
	if err == nil {
		t.Fatal("expected external error")
	}
	if err.Error() != "remote failed" {
		t.Errorf("error = %q, want %q", err.Error(), "remote failed")
	}
}

func TestRegistry_Execute_NoMiddleware_NoHandler(t *testing.T) {
	r := NewRegistry()
	r.Merge([]schema.ToolDef{{Name: "orphan"}})

	_, err := r.Execute(context.Background(), "orphan", "")
	if err == nil {
		t.Fatal("expected no-handler error")
	}
	if err.Error() != `tool "orphan" has no handler` {
		t.Errorf("error = %q, want no-handler message", err.Error())
	}
}

func TestRegistry_ExecuteMiddleware_Order_SameOption(t *testing.T) {
	var trace []string
	r := NewRegistry(WithExecuteMiddleware(
		tracingExecuteMiddleware(&trace, "A"),
		tracingExecuteMiddleware(&trace, "B"),
	))
	_ = r.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		trace = append(trace, "handler")
		return schema.TextResult("", "ok"), nil
	})

	if _, err := r.Execute(context.Background(), "echo", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{"A:before", "B:before", "handler", "B:after", "A:after"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestRegistry_ExecuteMiddleware_Order_AppendedOptions(t *testing.T) {
	var trace []string
	r := NewRegistry(
		WithExecuteMiddleware(tracingExecuteMiddleware(&trace, "A")),
		WithExecuteMiddleware(tracingExecuteMiddleware(&trace, "B")),
	)
	_ = r.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		trace = append(trace, "handler")
		return schema.TextResult("", "ok"), nil
	})

	if _, err := r.Execute(context.Background(), "echo", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{"A:before", "B:before", "handler", "B:after", "A:after"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestRegistry_ExecuteMiddleware_SkipsNil(t *testing.T) {
	var trace []string
	r := NewRegistry(WithExecuteMiddleware(
		nil,
		tracingExecuteMiddleware(&trace, "A"),
		nil,
	))
	_ = r.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		trace = append(trace, "handler")
		return schema.TextResult("", "ok"), nil
	})

	if _, err := r.Execute(context.Background(), "echo", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{"A:before", "handler", "A:after"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestRegistry_ExecuteMiddleware_ObservesNameAndArgs(t *testing.T) {
	var seenName, seenArgs string
	mw := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, name, args string) (schema.ToolResult, error) {
			seenName, seenArgs = name, args
			return next(ctx, name, args)
		}
	})
	r := NewRegistry(WithExecuteMiddleware(mw))
	_ = r.Register(schema.ToolDef{Name: "echo"}, echoHandler)

	if _, err := r.Execute(context.Background(), "echo", `{"x":1}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seenName != "echo" || seenArgs != `{"x":1}` {
		t.Errorf("observed (%q, %q), want (echo, {\"x\":1})", seenName, seenArgs)
	}
}

func TestRegistry_ExecuteMiddleware_RewriteResult(t *testing.T) {
	mw := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, name, args string) (schema.ToolResult, error) {
			result, err := next(ctx, name, args)
			if err != nil {
				return result, err
			}
			return schema.TextResult("", "redacted"), nil
		}
	})
	r := NewRegistry(WithExecuteMiddleware(mw))
	_ = r.Register(schema.ToolDef{Name: "echo"}, echoHandler)

	result, err := r.Execute(context.Background(), "echo", "secret")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Text() != "redacted" {
		t.Errorf("result = %q, want %q", result.Text(), "redacted")
	}
}

func TestRegistry_ExecuteMiddleware_RewriteErrorToResult(t *testing.T) {
	mw := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, name, args string) (schema.ToolResult, error) {
			_, err := next(ctx, name, args)
			if err != nil {
				return schema.ErrorResult("", "sanitised failure"), nil
			}
			return schema.TextResult("", "ok"), nil
		}
	})
	r := NewRegistry(WithExecuteMiddleware(mw))
	_ = r.Register(schema.ToolDef{Name: "fail"}, errorHandler)

	result, err := r.Execute(context.Background(), "fail", "")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.IsError || result.Text() != "sanitised failure" {
		t.Errorf("result = %+v, want IsError sanitised failure", result)
	}
}

func TestRegistry_ExecuteMiddleware_RewriteError(t *testing.T) {
	mw := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, name, args string) (schema.ToolResult, error) {
			_, err := next(ctx, name, args)
			if err != nil {
				return schema.ToolResult{}, errors.New("wrapped: " + err.Error())
			}
			return schema.TextResult("", "ok"), nil
		}
	})
	r := NewRegistry(WithExecuteMiddleware(mw))
	_ = r.Register(schema.ToolDef{Name: "fail"}, errorHandler)

	_, err := r.Execute(context.Background(), "fail", "")
	if err == nil {
		t.Fatal("expected rewritten error")
	}
	if err.Error() != "wrapped: tool failed" {
		t.Errorf("error = %q, want wrapped message", err.Error())
	}
}

func TestRegistry_ExecuteMiddleware_RunsOnceForLocalExternalAndDispatchErrors(t *testing.T) {
	var runs int
	audit := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, name, args string) (schema.ToolResult, error) {
			runs++
			return next(ctx, name, args)
		}
	})

	t.Run("local", func(t *testing.T) {
		runs = 0
		r := NewRegistry(WithExecuteMiddleware(audit))
		_ = r.Register(schema.ToolDef{Name: "echo"}, echoHandler)
		if _, err := r.Execute(context.Background(), "echo", ""); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if runs != 1 {
			t.Errorf("runs = %d, want 1", runs)
		}
	})

	t.Run("external", func(t *testing.T) {
		runs = 0
		ext := &recordingExternalCaller{result: schema.TextResult("", "ok")}
		r := NewRegistry(WithExternalCaller(ext), WithExecuteMiddleware(audit))
		r.Merge([]schema.ToolDef{{Name: "remote"}})
		if _, err := r.Execute(context.Background(), "remote", ""); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if runs != 1 {
			t.Errorf("runs = %d, want 1", runs)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		runs = 0
		r := NewRegistry(WithExecuteMiddleware(audit))
		_, err := r.Execute(context.Background(), "missing", "")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		if runs != 1 {
			t.Errorf("runs = %d, want 1", runs)
		}
	})
}

func TestRegistry_ExecuteMiddleware_ShortCircuitDeniesHandler(t *testing.T) {
	handlerCalls := 0
	deny := ExecuteMiddleware(func(_ ToolHandler) ToolHandler {
		return func(_ context.Context, name, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{}, fmt.Errorf("denied: %s", name)
		}
	})
	r := NewRegistry(WithExecuteMiddleware(deny))
	_ = r.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		handlerCalls++
		return schema.TextResult("", "should not run"), nil
	})

	_, err := r.Execute(context.Background(), "echo", "")
	if err == nil || err.Error() != "denied: echo" {
		t.Fatalf("error = %v, want denied: echo", err)
	}
	if handlerCalls != 0 {
		t.Errorf("handlerCalls = %d, want 0", handlerCalls)
	}
}

func TestRegistry_ExecuteMiddleware_ShortCircuitDeniesExternal(t *testing.T) {
	extCalls := 0
	deny := ExecuteMiddleware(func(_ ToolHandler) ToolHandler {
		return func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{}, errors.New("denied")
		}
	})
	ext := &recordingExternalCaller{calls: &extCalls, result: schema.TextResult("", "ok")}
	r := NewRegistry(WithExternalCaller(ext), WithExecuteMiddleware(deny))
	r.Merge([]schema.ToolDef{{Name: "remote"}})

	_, err := r.Execute(context.Background(), "remote", "")
	if err == nil || err.Error() != "denied" {
		t.Fatalf("error = %v, want denied", err)
	}
	if extCalls != 0 {
		t.Errorf("extCalls = %d, want 0", extCalls)
	}
}

func TestRegistry_ExecuteMiddleware_RewriteArgs(t *testing.T) {
	var gotName, gotArgs string
	rewrite := ExecuteMiddleware(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, _ /* name */, _ /* args */ string) (schema.ToolResult, error) {
			return next(ctx, "echo", `{"rewritten":true}`)
		}
	})
	r := NewRegistry(WithExecuteMiddleware(rewrite))
	_ = r.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, name, args string) (schema.ToolResult, error) {
		gotName, gotArgs = name, args
		return schema.TextResult("", "ok"), nil
	})

	if _, err := r.Execute(context.Background(), "other", `{"raw":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotName != "echo" || gotArgs != `{"rewritten":true}` {
		t.Errorf("handler got (%q, %q), want rewritten values", gotName, gotArgs)
	}
}

func TestRegistry_ExecuteMiddleware_SeesLiveRegistration(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "late", "")
	if err == nil {
		t.Fatal("expected not-found before register")
	}

	_ = r.Register(schema.ToolDef{Name: "late"}, echoHandler)
	result, err := r.Execute(context.Background(), "late", "x")
	if err != nil {
		t.Fatalf("Execute after Register: %v", err)
	}
	if result.Text() != "late:x" {
		t.Errorf("result = %q, want late:x", result.Text())
	}
}
