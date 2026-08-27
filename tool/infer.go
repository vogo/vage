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
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/vogo/vage/schema"
)

// Infer derives a schema.ToolDef and ToolHandler from a parameter struct T,
// keeping the schema and the argument decoding contract in one place.
//
// Parameters is a JSON object schema inferred from T's exported fields: names
// follow json tags, fields without omitempty are required, and
// jsonschema_description tags become property descriptions. Scalars, structs,
// slices/arrays and string-keyed maps map recursively; pointer fields keep
// their element type and also allow null. The returned handler is bound to
// name and fn: it decodes args into a fresh T per call and forwards the
// original context and decoded value to fn.
//
// Types whose JSON shape cannot be derived — non-struct roots, non-string map
// keys, interfaces, recursive types, custom JSON marshaling — panic at
// construction with the tool name and offending type.
//
// Infer is purely additive: existing hand-written ToolDef/ToolHandler pairs,
// the Registry, and existing tools are unaffected.
func Infer[T any](name, desc string, fn func(context.Context, T) (schema.ToolResult, error)) (schema.ToolDef, ToolHandler) {
	if fn == nil {
		panic(fmt.Sprintf("tool %q: Infer requires a non-nil handler function", name))
	}
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("tool %q: unsupported argument type %v: Infer requires a non-pointer struct type", name, t))
	}
	if implementsCustomJSON(t) {
		panic(fmt.Sprintf("tool %q: unsupported argument type %v: custom JSON marshaling cannot be inferred", name, t))
	}

	def := schema.ToolDef{
		Name:        name,
		Description: desc,
		Source:      schema.ToolSourceLocal,
		Parameters:  (&schemaBuilder{name: name, visiting: make(map[reflect.Type]bool)}).structSchema(t),
	}

	handler := func(ctx context.Context, _ string, args string) (schema.ToolResult, error) {
		var in T
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return schema.ToolResult{}, fmt.Errorf("tool %q: invalid arguments: %w", name, err)
		}
		return fn(ctx, in)
	}

	return def, handler
}

var (
	jsonMarshalerType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// implementsCustomJSON reports whether t or *t has custom JSON marshaling;
// encoding/json then bypasses field walking, so no schema can be inferred.
func implementsCustomJSON(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) ||
		reflect.PointerTo(t).Implements(jsonMarshalerType) ||
		t.Implements(jsonUnmarshalerType) ||
		reflect.PointerTo(t).Implements(jsonUnmarshalerType)
}

// fieldSpec is one JSON-visible field mapped to a schema property.
type fieldSpec struct {
	name     string
	schema   any
	required bool
}

// schemaBuilder reflects a struct into a JSON schema. It carries the tool name
// for panic messages and a visiting set that detects recursive types.
type schemaBuilder struct {
	name     string
	visiting map[reflect.Type]bool
}

// structSchema derives a JSON object schema for struct type t.
func (b *schemaBuilder) structSchema(t reflect.Type) any {
	if b.visiting[t] {
		panic(fmt.Sprintf("tool %q: unsupported recursive type %v", b.name, t))
	}
	b.visiting[t] = true
	defer delete(b.visiting, t)

	properties := make(map[string]any)
	required := []string{}
	for _, f := range b.fields(t, "") {
		if _, exists := properties[f.name]; exists {
			// Direct fields precede promoted ones, so the first wins.
			continue
		}
		properties[f.name] = f.schema
		if f.required {
			required = append(required, f.name)
		}
	}

	node := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		node["required"] = required
	}
	return node
}

// fields returns JSON-visible fields in precedence order: direct fields first,
// then fields promoted from anonymous embedded structs. path is for panics.
func (b *schemaBuilder) fields(t reflect.Type, path string) []fieldSpec {
	var direct, promoted []fieldSpec

	for sf := range t.Fields() {
		// Anonymous embedded structs (even with an unexported type name) are
		// flattened by encoding/json; other unexported fields are invisible.
		et := sf.Type
		if et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if !sf.IsExported() {
			if !sf.Anonymous || et.Kind() != reflect.Struct {
				continue
			}
		}

		jsonName, skip, opts := parseJSONTag(sf)
		if skip {
			continue
		}

		fp := sf.Name
		if path != "" {
			fp = path + "." + sf.Name
		}

		// Flatten unnamed anonymous structs like encoding/json.
		if sf.Anonymous {
			if jsonName == "" && !implementsCustomJSON(sf.Type) && et.Kind() == reflect.Struct {
				promoted = append(promoted, b.fields(et, fp)...)
				continue
			}
			if jsonName == "" {
				jsonName = sf.Type.Name()
			}
		}
		if jsonName == "" {
			jsonName = sf.Name
		}

		node := b.typeSchema(fp, sf.Type)
		if desc := sf.Tag.Get("jsonschema_description"); desc != "" {
			pm, ok := node.(map[string]any)
			if !ok {
				pm = map[string]any{"type": node}
			}
			pm["description"] = desc
			node = pm
		}

		direct = append(direct, fieldSpec{
			name:     jsonName,
			schema:   node,
			required: !opts.omitempty,
		})
	}

	return append(direct, promoted...)
}

// typeSchema maps a Go type to a JSON schema node.
func (b *schemaBuilder) typeSchema(path string, t reflect.Type) any {
	if implementsCustomJSON(t) {
		panic(fmt.Sprintf("tool %q: field %q: unsupported type %v: custom JSON marshaling cannot be inferred", b.name, path, t))
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		// encoding/json encodes []byte as a base64 string.
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "array", "items": b.typeSchema(path, t.Elem())}
	case reflect.Array:
		return map[string]any{"type": "array", "items": b.typeSchema(path, t.Elem())}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("tool %q: field %q: unsupported type %v: map keys must be strings", b.name, path, t))
		}
		return map[string]any{"type": "object", "additionalProperties": b.typeSchema(path, t.Elem())}
	case reflect.Pointer:
		return makeNullable(b.typeSchema(path, t.Elem()))
	case reflect.Struct:
		return b.structSchema(t)
	default:
		panic(fmt.Sprintf("tool %q: field %q: unsupported type %v", b.name, path, t))
	}
}

type jsonTagOpts struct {
	omitempty bool
}

// parseJSONTag extracts the JSON name and options from a field's json tag;
// skip marks fields excluded via json:"-".
func parseJSONTag(sf reflect.StructField) (name string, skip bool, opts jsonTagOpts) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", true, opts
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, o := range parts[1:] {
		if o == "omitempty" {
			opts.omitempty = true
		}
	}
	return name, false, opts
}

// makeNullable marks a node as also accepting null (a nil pointer). Pointer
// nesting collapses to one null level, matching encoding/json.
func makeNullable(node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return map[string]any{"type": []any{node, "null"}}
	}
	switch typ := m["type"].(type) {
	case string:
		m["type"] = []any{typ, "null"}
	}
	return m
}
