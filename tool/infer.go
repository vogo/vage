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

// Compile derives a schema.ToolDef and ToolHandler from a parameter struct T
// and returns a construction error instead of panicking.
//
// Use Compile when a single illegal definition should be skipped — dynamic
// discovery, batch registration, or any caller that can handle the error.
// On failure the ToolDef is zero and the handler is nil; do not register or
// invoke that result. Type-inference errors include the tool name and the
// unsupported Go type; field-level errors also keep the field path.
//
// Parameters is a JSON object schema inferred from T's exported fields: names
// follow json tags, fields without omitempty are required, and descriptions
// come from the jsonschema_description tag (falling back to description).
// Scalars, structs, slices/arrays and string-keyed maps map recursively;
// pointer fields keep their element type and also allow null. The returned
// handler is bound to name and fn: it decodes args into a fresh T per call and
// forwards the original context and decoded value to fn.
//
// JSON decoding errors and business-function errors are handler errors, not
// construction errors; Compile does not change their wrapping or call
// semantics. Schema inference itself is unchanged: types whose JSON shape
// cannot be derived still fail construction.
func Compile[T any](name, desc string, fn func(context.Context, T) (schema.ToolResult, error)) (schema.ToolDef, ToolHandler, error) {
	if fn == nil {
		return schema.ToolDef{}, nil, fmt.Errorf("tool %q: Infer requires a non-nil handler function", name)
	}
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		return schema.ToolDef{}, nil, fmt.Errorf("tool %q: unsupported argument type %v: Infer requires a non-pointer struct type", name, t)
	}
	if implementsCustomJSON(t) {
		return schema.ToolDef{}, nil, fmt.Errorf("tool %q: unsupported argument type %v: custom JSON marshaling cannot be inferred", name, t)
	}

	params, err := (&schemaBuilder{name: name, visiting: make(map[reflect.Type]bool)}).structSchema(t)
	if err != nil {
		return schema.ToolDef{}, nil, err
	}

	def := schema.ToolDef{
		Name:        name,
		Description: desc,
		Source:      schema.ToolSourceLocal,
		Parameters:  params,
	}

	handler := func(ctx context.Context, _ string, args string) (schema.ToolResult, error) {
		var in T
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return schema.ToolResult{}, fmt.Errorf("tool %q: invalid arguments: %w", name, err)
		}
		return fn(ctx, in)
	}

	return def, handler, nil
}

// MustInfer derives a schema.ToolDef and ToolHandler from a parameter struct T
// and panics if construction fails. It is Compile plus panic, for static
// declarations where an illegal parameter type is a programming mistake.
//
// Prefer Compile when the caller can recover from one bad definition. Infer
// remains the compatibility panic entry and delegates here; new static
// declarations should call MustInfer so the name states that failure is fatal.
func MustInfer[T any](name, desc string, fn func(context.Context, T) (schema.ToolResult, error)) (schema.ToolDef, ToolHandler) {
	def, handler, err := Compile(name, desc, fn)
	if err != nil {
		// Panic the formatted string, not the error value, so the recoverable
		// payload stays identical to the pre-Compile Infer constructor.
		panic(err.Error())
	}
	return def, handler
}

// Infer derives a schema.ToolDef and ToolHandler from a parameter struct T,
// keeping the schema and the argument decoding contract in one place.
//
// Infer is the compatibility constructor: it keeps the original signature and
// panics at construction when the JSON shape of T cannot be derived — non-struct
// roots, non-string map keys, interfaces, recursive types, custom JSON
// marshaling, or a nil handler function — with the tool name and offending
// type. It delegates to MustInfer. Existing call sites do not need to migrate.
//
// New static declarations should prefer MustInfer so the name states that
// failure is fatal. Dynamic or batch registration should use Compile, which
// returns the same successful product and an error instead of panicking.
//
// Infer is purely additive: existing hand-written ToolDef/ToolHandler pairs,
// the Registry, and existing tools are unaffected.
func Infer[T any](name, desc string, fn func(context.Context, T) (schema.ToolResult, error)) (schema.ToolDef, ToolHandler) {
	return MustInfer(name, desc, fn)
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
// for error messages and a visiting set that detects recursive types.
type schemaBuilder struct {
	name     string
	visiting map[reflect.Type]bool
}

// structSchema derives a JSON object schema for struct type t.
func (b *schemaBuilder) structSchema(t reflect.Type) (any, error) {
	if b.visiting[t] {
		return nil, fmt.Errorf("tool %q: unsupported recursive type %v", b.name, t)
	}
	b.visiting[t] = true
	defer delete(b.visiting, t)

	fields, err := b.fields(t, "")
	if err != nil {
		return nil, err
	}

	properties := make(map[string]any)
	required := []string{}
	for _, f := range fields {
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
	return node, nil
}

// fields returns JSON-visible fields in precedence order: direct fields first,
// then fields promoted from anonymous embedded structs. path is for errors.
func (b *schemaBuilder) fields(t reflect.Type, path string) ([]fieldSpec, error) {
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
				nested, err := b.fields(et, fp)
				if err != nil {
					return nil, err
				}
				promoted = append(promoted, nested...)
				continue
			}
			if jsonName == "" {
				jsonName = sf.Type.Name()
			}
		}
		if jsonName == "" {
			jsonName = sf.Name
		}

		node, err := b.typeSchema(fp, sf.Type)
		if err != nil {
			return nil, err
		}
		// Prefer jsonschema_description, falling back to description.
		desc := sf.Tag.Get("jsonschema_description")
		if desc == "" {
			desc = sf.Tag.Get("description")
		}
		if desc != "" {
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

	return append(direct, promoted...), nil
}

// typeSchema maps a Go type to a JSON schema node.
func (b *schemaBuilder) typeSchema(path string, t reflect.Type) (any, error) {
	if implementsCustomJSON(t) {
		return nil, fmt.Errorf("tool %q: field %q: unsupported type %v: custom JSON marshaling cannot be inferred", b.name, path, t)
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Slice:
		// encoding/json encodes []byte as a base64 string.
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}, nil
		}
		items, err := b.typeSchema(path, t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Array:
		items, err := b.typeSchema(path, t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("tool %q: field %q: unsupported type %v: map keys must be strings", b.name, path, t)
		}
		values, err := b.typeSchema(path, t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Pointer:
		inner, err := b.typeSchema(path, t.Elem())
		if err != nil {
			return nil, err
		}
		return makeNullable(inner), nil
	case reflect.Struct:
		return b.structSchema(t)
	default:
		return nil, fmt.Errorf("tool %q: field %q: unsupported type %v", b.name, path, t)
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
