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

package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

type inferInner struct {
	Enabled bool    `json:"enabled"`
	Ratio   float64 `json:"ratio,omitempty"`
}

type inferArgs struct {
	City    string            `json:"city" jsonschema_description:"City to look up"`
	Count   int               `json:"count"`
	Units   string            `json:"units,omitempty"`
	Temp    float64           `json:"temperature,omitempty"`
	Secret  string            `json:"-"`
	hidden  int               // unexported, must not appear in the schema
	Labels  []string          `json:"labels,omitempty"`
	Matrix  [][]int           `json:"matrix,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
	Nested  inferInner        `json:"nested"`
	Maybe   *int              `json:"maybe,omitempty"`
	Pointer *inferInner       `json:"pointer,omitempty"`
}

// TestInfer_BuildsObjectSchema asserts the reflected schema matches the
// inference rules for tags, required, descriptions, and nested types.
func TestInfer_BuildsObjectSchema(t *testing.T) {
	noop := func(context.Context, inferArgs) (schema.ToolResult, error) { return schema.ToolResult{}, nil }
	def, _ := tool.Infer("weather", "look up weather", noop)

	if def.Name != "weather" {
		t.Errorf("def.Name = %q, want %q", def.Name, "weather")
	}
	if def.Description != "look up weather" {
		t.Errorf("def.Description = %q, want %q", def.Description, "look up weather")
	}
	if def.Source != schema.ToolSourceLocal {
		t.Errorf("def.Source = %q, want %q", def.Source, schema.ToolSourceLocal)
	}

	inner := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"enabled": map[string]any{"type": "boolean"}, "ratio": map[string]any{"type": "number"}},
		"required":             []string{"enabled"},
		"additionalProperties": false,
	}

	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"city", "count", "nested"},
		"properties": map[string]any{
			"city":        map[string]any{"type": "string", "description": "City to look up"},
			"count":       map[string]any{"type": "integer"},
			"units":       map[string]any{"type": "string"},
			"temperature": map[string]any{"type": "number"},
			"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"matrix":      map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}},
			"meta":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"nested":      inner,
			"maybe":       map[string]any{"type": []any{"integer", "null"}},
			"pointer":     map[string]any{"type": []any{"object", "null"}, "properties": inner["properties"], "required": inner["required"], "additionalProperties": false},
		},
	}

	if !reflect.DeepEqual(def.Parameters, want) {
		t.Errorf("Parameters mismatch:\n got: %#v\nwant: %#v", def.Parameters, want)
	}

	// Must survive JSON serialization (the adapters pass Parameters through).
	if _, err := json.Marshal(def.Parameters); err != nil {
		t.Errorf("json.Marshal(Parameters) error: %v", err)
	}
}

// TestInfer_DescriptionTagFallback asserts descriptions come from
// jsonschema_description, falling back to the plain description tag.
func TestInfer_DescriptionTagFallback(t *testing.T) {
	type args struct {
		FromDesc     string `json:"from_desc" description:"from description tag"`
		PreferSchema string `json:"prefer_schema" jsonschema_description:"from jsonschema_description" description:"from description tag"`
		None         string `json:"none"`
	}
	def, _ := tool.Infer("x", "", func(context.Context, args) (schema.ToolResult, error) {
		return schema.ToolResult{}, nil
	})
	props := def.Parameters.(map[string]any)["properties"].(map[string]any)

	if got := props["from_desc"].(map[string]any)["description"]; got != "from description tag" {
		t.Errorf("description tag fallback = %v, want %q", got, "from description tag")
	}
	if got := props["prefer_schema"].(map[string]any)["description"]; got != "from jsonschema_description" {
		t.Errorf("jsonschema_description must win over description, got %v", got)
	}
	if _, ok := props["none"].(map[string]any)["description"]; ok {
		t.Error("no description expected without a description tag")
	}
}

func TestInfer_FlattensEmbeddedStructs(t *testing.T) {
	t.Run("unnamed embedded struct is flattened", func(t *testing.T) {
		type embedded struct {
			A int    `json:"a"`
			B string `json:"b,omitempty"`
		}
		type outer struct {
			embedded
			C bool `json:"c"`
		}
		def, _ := tool.Infer("x", "", func(context.Context, outer) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
		params := def.Parameters.(map[string]any)
		props := params["properties"].(map[string]any)
		if len(props) != 3 {
			t.Fatalf("flattened properties = %d, want 3 (%v)", len(props), props)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, ok := props[name]; !ok {
				t.Errorf("missing promoted property %q", name)
			}
		}
		req := params["required"].([]string)
		if !reflect.DeepEqual(req, []string{"c", "a"}) {
			t.Errorf("required = %v, want [c a] (b is omitempty; direct fields precede promoted ones)", req)
		}
	})

	t.Run("named embedded struct becomes a nested object", func(t *testing.T) {
		type embedded struct {
			A int `json:"a"`
		}
		type outer struct {
			embedded `json:"inner"`
			C        bool `json:"c"`
		}
		def, _ := tool.Infer("x", "", func(context.Context, outer) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
		params := def.Parameters.(map[string]any)
		props := params["properties"].(map[string]any)
		if _, ok := props["a"]; ok {
			t.Error("named embedded struct must not flatten its fields")
		}
		inner, ok := props["inner"].(map[string]any)
		if !ok {
			t.Fatalf("expected nested object property %q", "inner")
		}
		if inner["type"] != "object" {
			t.Errorf("inner type = %v, want object", inner["type"])
		}
	})

	t.Run("direct field wins over promoted field", func(t *testing.T) {
		type embedded struct {
			Name int `json:"name"`
		}
		type outer struct {
			embedded
			Name string `json:"name"` // shadows the promoted int field
		}
		def, _ := tool.Infer("x", "", func(context.Context, outer) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
		params := def.Parameters.(map[string]any)
		props := params["properties"].(map[string]any)
		name := props["name"].(map[string]any)
		if name["type"] != "string" {
			t.Errorf("name type = %v, want string (direct field must shadow promoted field)", name["type"])
		}
	})
}

func TestInfer_HandlerSuccess(t *testing.T) {
	calls := 0
	wantCtx := context.WithValue(context.Background(), inferCtxKey("k"), "v")

	def, handler := tool.Infer("echo", "echo tool", func(ctx context.Context, a inferArgs) (schema.ToolResult, error) {
		calls++
		if ctx != wantCtx {
			t.Error("fn received a different context than the one passed to the handler")
		}
		if a.City != "Paris" || a.Count != 3 || a.Units != "metric" {
			t.Errorf("fn received unexpected arguments: %+v", a)
		}
		if a.hidden != 0 {
			t.Errorf("unexported field must stay untouched by decoding, got %d", a.hidden)
		}
		return schema.TextResult("", fmt.Sprintf("%s:%d", a.City, a.Count)), nil
	})

	if def.Name != "echo" {
		t.Errorf("def.Name = %q, want %q", def.Name, "echo")
	}

	// The name passed at dispatch is ignored.
	res, err := handler(wantCtx, "some-other-name", `{"city":"Paris","count":3,"units":"metric"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if got := res.Content[0].Text; got != "Paris:3" {
		t.Errorf("result = %q, want %q", got, "Paris:3")
	}
	if res.IsError {
		t.Error("result should not be an error result")
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
}

func TestInfer_HandlerReturnsFnErrorUnchanged(t *testing.T) {
	sentinel := errors.New("boom")
	def, handler := tool.Infer("fail", "", func(context.Context, inferArgs) (schema.ToolResult, error) {
		return schema.TextResult("", "partial"), sentinel
	})
	_ = def

	res, err := handler(context.Background(), "fail", `{"city":"x","count":1}`)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the fn's sentinel error untouched", err)
	}
	if got := res.Content[0].Text; got != "partial" {
		t.Errorf("result = %q, want fn's result untouched %q", got, "partial")
	}
}

// TestInfer_HandlerInvalidArguments guards the decode-failure contract.
func TestInfer_HandlerInvalidArguments(t *testing.T) {
	ran := false
	_, handler := tool.Infer("echo", "", func(context.Context, inferArgs) (schema.ToolResult, error) {
		ran = true
		return schema.ToolResult{}, nil
	})

	malformed := []struct {
		name string
		args string
		as   func(error) bool
	}{
		{"syntax error", `{"city":`, func(err error) bool {
			var se *json.SyntaxError
			return errors.As(err, &se)
		}},
		{"type mismatch", `{"city":"x","count":"not-an-int"}`, func(err error) bool {
			var ue *json.UnmarshalTypeError
			return errors.As(err, &ue)
		}},
	}

	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			res, err := handler(context.Background(), "echo", tc.args)
			if err == nil {
				t.Fatal("expected error for malformed arguments")
			}
			if !strings.Contains(err.Error(), `tool "echo": invalid arguments:`) {
				t.Errorf("error %q missing the tool-name context", err.Error())
			}
			if !tc.as(err) {
				t.Errorf("underlying JSON error not on the error chain: %v", err)
			}
			if !reflect.DeepEqual(res, schema.ToolResult{}) {
				t.Errorf("result = %+v, want zero ToolResult", res)
			}
			if ran {
				t.Fatal("fn must not be called when decoding fails")
			}
		})
	}
}

func TestInfer_CoexistsWithHandWrittenTools(t *testing.T) {
	reg := tool.NewRegistry()

	def, handler := tool.Infer("inferred", "generated tool", func(ctx context.Context, a inferArgs) (schema.ToolResult, error) {
		return schema.TextResult("", a.City), nil
	})
	if err := reg.Register(def, handler); err != nil {
		t.Fatalf("Register(inferred): %v", err)
	}

	manualDef := schema.ToolDef{Name: "manual", Description: "hand written", Parameters: map[string]any{"type": "object"}}
	if err := reg.Register(manualDef, func(context.Context, string, string) (schema.ToolResult, error) {
		return schema.TextResult("", "pong"), nil
	}); err != nil {
		t.Fatalf("Register(manual): %v", err)
	}

	res, err := reg.Execute(context.Background(), "inferred", `{"city":"Rome","count":2}`)
	if err != nil {
		t.Fatalf("Execute(inferred): %v", err)
	}
	if got := res.Content[0].Text; got != "Rome" {
		t.Errorf("inferred result = %q, want %q", got, "Rome")
	}

	res, err = reg.Execute(context.Background(), "manual", `{}`)
	if err != nil {
		t.Fatalf("Execute(manual): %v", err)
	}
	if got := res.Content[0].Text; got != "pong" {
		t.Errorf("manual result = %q, want %q", got, "pong")
	}
}

// requirePanic asserts f panics with a message containing wantSub.
func requirePanic(t *testing.T, wantSub string, f func()) {
	t.Helper()
	msg := fmt.Sprint(panicValue(t, f))
	if !strings.Contains(msg, wantSub) {
		t.Fatalf("panic %q does not contain %q", msg, wantSub)
	}
}

// panicValue runs f and returns the recovered panic payload.
func panicValue(t *testing.T, f func()) any {
	t.Helper()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		f()
	}()
	if recovered == nil {
		t.Fatal("expected panic, got none")
	}
	return recovered
}

func TestInfer_PanicsOnUnsupportedTopLevel(t *testing.T) {
	requirePanic(t, `tool "x": unsupported argument type int`, func() {
		tool.Infer("x", "", func(context.Context, int) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
	})

	requirePanic(t, `tool "x": unsupported argument type *tool_test.inferArgs`, func() {
		tool.Infer("x", "", func(context.Context, *inferArgs) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
	})

	requirePanic(t, "requires a non-pointer struct type", func() {
		tool.Infer("x", "", func(context.Context, []inferArgs) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
	})
}

func TestInfer_PanicsOnUnsupportedFieldType(t *testing.T) {
	t.Run("non-string map key", func(t *testing.T) {
		type bad struct {
			M map[int]string `json:"m"`
		}
		requirePanic(t, `tool "x": field "M": unsupported type map[int]string`, func() {
			tool.Infer("x", "", noopFn[bad]())
		})
	})

	t.Run("interface field", func(t *testing.T) {
		type bad struct {
			V any `json:"v"`
		}
		requirePanic(t, `tool "x": field "V": unsupported type interface`, func() {
			tool.Infer("x", "", noopFn[bad]())
		})
	})

	t.Run("custom JSON marshaling", func(t *testing.T) {
		type bad struct {
			T time.Time `json:"t"`
		}
		requirePanic(t, `tool "x": field "T": unsupported type time.Time: custom JSON marshaling`, func() {
			tool.Infer("x", "", noopFn[bad]())
		})
	})

	t.Run("chan field", func(t *testing.T) {
		type bad struct {
			Ch chan int `json:"ch"`
		}
		requirePanic(t, `tool "x": field "Ch": unsupported type chan int`, func() {
			tool.Infer("x", "", noopFn[bad]())
		})
	})

	t.Run("root with custom marshaling", func(t *testing.T) {
		requirePanic(t, `tool "x": unsupported argument type tool_test.marshalRoot: custom JSON marshaling`, func() {
			tool.Infer("x", "", noopFn[marshalRoot]())
		})
	})

	t.Run("recursive type", func(t *testing.T) {
		type treeNode struct {
			Value    string      `json:"value"`
			Children []*treeNode `json:"children,omitempty"`
		}
		requirePanic(t, `tool "x": unsupported recursive type tool_test.treeNode`, func() {
			tool.Infer("x", "", noopFn[treeNode]())
		})
	})
}

func TestInfer_PanicsOnNilFunction(t *testing.T) {
	requirePanic(t, `tool "x": Infer requires a non-nil handler function`, func() {
		tool.Infer[inferArgs]("x", "", nil)
	})
}

func TestMustInfer_PanicMatchesInfer(t *testing.T) {
	type badMap struct {
		M map[int]string `json:"m"`
	}
	type badAny struct {
		V any `json:"v"`
	}
	type badTime struct {
		T time.Time `json:"t"`
	}
	type badChan struct {
		Ch chan int `json:"ch"`
	}
	type treeNode struct {
		Value    string      `json:"value"`
		Children []*treeNode `json:"children,omitempty"`
	}

	cases := []struct {
		name  string
		infer func()
		must  func()
	}{
		{
			name:  "nil function",
			infer: func() { tool.Infer[inferArgs]("x", "", nil) },
			must:  func() { tool.MustInfer[inferArgs]("x", "", nil) },
		},
		{
			name: "int root",
			infer: func() {
				tool.Infer("x", "", func(context.Context, int) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
			must: func() {
				tool.MustInfer("x", "", func(context.Context, int) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
		},
		{
			name: "pointer root",
			infer: func() {
				tool.Infer("x", "", func(context.Context, *inferArgs) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
			must: func() {
				tool.MustInfer("x", "", func(context.Context, *inferArgs) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
		},
		{
			name:  "slice root",
			infer: func() { tool.Infer("x", "", noopFn[[]inferArgs]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[[]inferArgs]()) },
		},
		{
			name:  "non-string map key",
			infer: func() { tool.Infer("x", "", noopFn[badMap]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[badMap]()) },
		},
		{
			name:  "interface field",
			infer: func() { tool.Infer("x", "", noopFn[badAny]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[badAny]()) },
		},
		{
			name:  "custom JSON marshaling",
			infer: func() { tool.Infer("x", "", noopFn[badTime]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[badTime]()) },
		},
		{
			name:  "chan field",
			infer: func() { tool.Infer("x", "", noopFn[badChan]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[badChan]()) },
		},
		{
			name:  "root with custom marshaling",
			infer: func() { tool.Infer("x", "", noopFn[marshalRoot]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[marshalRoot]()) },
		},
		{
			name:  "recursive type",
			infer: func() { tool.Infer("x", "", noopFn[treeNode]()) },
			must:  func() { tool.MustInfer("x", "", noopFn[treeNode]()) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotInfer := panicValue(t, tc.infer)
			gotMust := panicValue(t, tc.must)
			if !reflect.DeepEqual(gotInfer, gotMust) {
				t.Errorf("Infer panic %#v (%T), MustInfer panic %#v (%T)", gotInfer, gotInfer, gotMust, gotMust)
			}
			if _, ok := gotInfer.(string); !ok {
				t.Errorf("panic payload type %T, want string (pre-Compile Infer payload)", gotInfer)
			}
		})
	}
}

func TestMustInfer_SuccessMatchesInfer(t *testing.T) {
	noop := func(context.Context, inferArgs) (schema.ToolResult, error) { return schema.ToolResult{}, nil }
	defMust, handlerMust := tool.MustInfer("weather", "look up weather", noop)
	defInfer, handlerInfer := tool.Infer("weather", "look up weather", noop)
	if !reflect.DeepEqual(defMust, defInfer) {
		t.Errorf("MustInfer ToolDef mismatch vs Infer:\n got: %#v\nwant: %#v", defMust, defInfer)
	}
	if handlerMust == nil || handlerInfer == nil {
		t.Fatal("successful MustInfer/Infer must return a handler")
	}
}

func TestCompile_ReturnsErrorOnUnsupportedTypes(t *testing.T) {
	type badMap struct {
		M map[int]string `json:"m"`
	}
	type badAny struct {
		V any `json:"v"`
	}
	type badTime struct {
		T time.Time `json:"t"`
	}
	type badChan struct {
		Ch chan int `json:"ch"`
	}
	type treeNode struct {
		Value    string      `json:"value"`
		Children []*treeNode `json:"children,omitempty"`
	}

	cases := []struct {
		name     string
		run      func() (schema.ToolDef, tool.ToolHandler, error)
		wantSubs []string
	}{
		{
			name: "nil function",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile[inferArgs]("weather", "", nil)
			},
			wantSubs: []string{`tool "weather"`, "non-nil handler function"},
		},
		{
			name: "int root",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", func(context.Context, int) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
			wantSubs: []string{`tool "x"`, "unsupported argument type int"},
		},
		{
			name: "pointer root",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", func(context.Context, *inferArgs) (schema.ToolResult, error) {
					return schema.ToolResult{}, nil
				})
			},
			wantSubs: []string{`tool "x"`, "unsupported argument type *tool_test.inferArgs"},
		},
		{
			name: "slice root",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[[]inferArgs]())
			},
			wantSubs: []string{`tool "x"`, "requires a non-pointer struct type"},
		},
		{
			name: "non-string map key",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[badMap]())
			},
			wantSubs: []string{`tool "x"`, `field "M"`, "unsupported type map[int]string"},
		},
		{
			name: "interface field",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[badAny]())
			},
			wantSubs: []string{`tool "x"`, `field "V"`, "unsupported type interface"},
		},
		{
			name: "custom JSON marshaling",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[badTime]())
			},
			wantSubs: []string{`tool "x"`, `field "T"`, "unsupported type time.Time", "custom JSON marshaling"},
		},
		{
			name: "chan field",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[badChan]())
			},
			wantSubs: []string{`tool "x"`, `field "Ch"`, "unsupported type chan int"},
		},
		{
			name: "root with custom marshaling",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[marshalRoot]())
			},
			wantSubs: []string{`tool "x"`, "unsupported argument type tool_test.marshalRoot", "custom JSON marshaling"},
		},
		{
			name: "recursive type",
			run: func() (schema.ToolDef, tool.ToolHandler, error) {
				return tool.Compile("x", "", noopFn[treeNode]())
			},
			wantSubs: []string{`tool "x"`, "unsupported recursive type tool_test.treeNode"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var def schema.ToolDef
			var handler tool.ToolHandler
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Compile panicked: %v", r)
					}
				}()
				def, handler, err = tc.run()
			}()
			if err == nil {
				t.Fatal("expected construction error")
			}
			if !reflect.DeepEqual(def, schema.ToolDef{}) {
				t.Errorf("def = %#v, want zero ToolDef", def)
			}
			if handler != nil {
				t.Error("handler must be nil on construction error")
			}
			msg := err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("error %q does not contain %q", msg, sub)
				}
			}
		})
	}
}

func TestCompile_MatchesInferOnSupportedType(t *testing.T) {
	newFn := func(calls *int, sentinel error) func(context.Context, inferArgs) (schema.ToolResult, error) {
		return func(_ context.Context, a inferArgs) (schema.ToolResult, error) {
			*calls++
			if sentinel != nil {
				return schema.TextResult("", "partial"), sentinel
			}
			return schema.TextResult("", fmt.Sprintf("%s:%d", a.City, a.Count)), nil
		}
	}

	var compileCalls, inferCalls int
	defC, handlerC, err := tool.Compile("echo", "echo tool", newFn(&compileCalls, nil))
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	defI, handlerI := tool.Infer("echo", "echo tool", newFn(&inferCalls, nil))
	if !reflect.DeepEqual(defC, defI) {
		t.Errorf("ToolDef mismatch:\n compile: %#v\n   infer: %#v", defC, defI)
	}

	args := `{"city":"Paris","count":3,"units":"metric"}`
	resC, errC := handlerC(context.Background(), "echo", args)
	resI, errI := handlerI(context.Background(), "echo", args)
	if errC != nil || errI != nil {
		t.Fatalf("handler errors compile=%v infer=%v", errC, errI)
	}
	if !reflect.DeepEqual(resC, resI) {
		t.Errorf("success result mismatch: compile=%+v infer=%+v", resC, resI)
	}
	if compileCalls != 1 || inferCalls != 1 {
		t.Errorf("fn calls compile=%d infer=%d, want 1 each", compileCalls, inferCalls)
	}

	malformed := `{"city":"x","count":"not-an-int"}`
	resC, errC = handlerC(context.Background(), "echo", malformed)
	resI, errI = handlerI(context.Background(), "echo", malformed)
	if errC == nil || errI == nil {
		t.Fatal("expected decode error from both handlers")
	}
	if errC.Error() != errI.Error() {
		t.Errorf("decode error mismatch:\n compile: %v\n   infer: %v", errC, errI)
	}
	var ueC, ueI *json.UnmarshalTypeError
	if !errors.As(errC, &ueC) || !errors.As(errI, &ueI) {
		t.Errorf("underlying JSON error missing: compile=%v infer=%v", errC, errI)
	}
	if !reflect.DeepEqual(resC, schema.ToolResult{}) || !reflect.DeepEqual(resI, schema.ToolResult{}) {
		t.Errorf("decode failure must return zero ToolResult, compile=%+v infer=%+v", resC, resI)
	}
	if compileCalls != 1 || inferCalls != 1 {
		t.Errorf("fn must not run on decode failure, calls compile=%d infer=%d", compileCalls, inferCalls)
	}

	sentinel := errors.New("boom")
	var compileFailCalls, inferFailCalls int
	_, handlerC, err = tool.Compile("fail", "", newFn(&compileFailCalls, sentinel))
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	_, handlerI = tool.Infer("fail", "", newFn(&inferFailCalls, sentinel))
	legal := `{"city":"x","count":1}`
	resC, errC = handlerC(context.Background(), "fail", legal)
	resI, errI = handlerI(context.Background(), "fail", legal)
	if !errors.Is(errC, sentinel) || !errors.Is(errI, sentinel) {
		t.Errorf("business error chain mismatch: compile=%v infer=%v", errC, errI)
	}
	if !reflect.DeepEqual(resC, resI) {
		t.Errorf("business-error result mismatch: compile=%+v infer=%+v", resC, resI)
	}
	if compileFailCalls != 1 || inferFailCalls != 1 {
		t.Errorf("fn calls compile=%d infer=%d, want 1 each", compileFailCalls, inferFailCalls)
	}
}

func TestCompile_FailedItemDoesNotBlockLaterRegistration(t *testing.T) {
	reg := tool.NewRegistry()

	_, _, err := tool.Compile("bad", "", func(context.Context, int) (schema.ToolResult, error) {
		return schema.ToolResult{}, nil
	})
	if err == nil {
		t.Fatal("expected error for illegal type")
	}

	def, handler, err := tool.Compile("echo", "echo tool", func(_ context.Context, a inferArgs) (schema.ToolResult, error) {
		return schema.TextResult("", a.City), nil
	})
	if err != nil {
		t.Fatalf("Compile(echo): %v", err)
	}
	if err := reg.Register(def, handler); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}

	res, err := reg.Execute(context.Background(), "echo", `{"city":"Rome","count":1}`)
	if err != nil {
		t.Fatalf("Execute(echo): %v", err)
	}
	if got := res.Content[0].Text; got != "Rome" {
		t.Errorf("result = %q, want %q", got, "Rome")
	}
	if _, ok := reg.Get("bad"); ok {
		t.Error("failed Compile must not register the illegal tool")
	}
}

// marshalRoot has a custom MarshalJSON, so its JSON shape is opaque.
type marshalRoot struct {
	X int `json:"x"`
}

func (marshalRoot) MarshalJSON() ([]byte, error) { return []byte("1"), nil }

// noopFn returns a handler that does nothing, for construction-panic tests.
func noopFn[T any]() func(context.Context, T) (schema.ToolResult, error) {
	return func(context.Context, T) (schema.ToolResult, error) { return schema.ToolResult{}, nil }
}

type inferCtxKey string
