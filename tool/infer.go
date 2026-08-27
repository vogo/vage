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

// Infer derives a schema.ToolDef and a ToolHandler from a parameter struct T,
// keeping the JSON Schema and the argument decoding contract in one place.
//
// The returned ToolDef carries the given name and description, marks itself as a
// local tool, and carries a JSON object schema inferred from T's exported
// fields; it can be registered through the existing Registry.Register (or
// RegisterIfAbsent), and any existing ToolDef metadata such as ReadOnly can be
// filled in before registration. The returned handler is bound to name and fn:
// it ignores the name passed at dispatch, decodes args into a fresh T per call,
// and forwards the original context and decoded value to fn.
//
// # Schema inference rules
//
// Only fields visible to encoding/json participate: field names follow the
// json tag (falling back to the Go field name), json:"-" and unexported fields
// are excluded, and fields without omitempty are listed in the schema's
// required array while fields with omitempty stay optional. A jsonschema_description
// tag on a field is written into that property's description. Scalars, structs,
// slices/arrays, and string-keyed maps map recursively to the corresponding JSON
// Schema types; pointer fields keep their element type and additionally allow
// null. Anonymous struct fields are flattened exactly as encoding/json promotes
// them, with directly declared fields taking precedence over promoted ones.
//
// Types whose JSON shape cannot be derived — non-struct root types, non-string
// map keys, interfaces, recursive types, and types with custom JSON marshaling —
// panic at construction with the tool name and offending type, rather than
// emitting a schema that would disagree with the real decoding behavior.
//
// Infer is purely additive: hand-written ToolDef/ToolHandler pairs, the
// Registry, and existing tools are unaffected.
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

// implementsCustomJSON reports whether the value or its pointer type declares
// custom JSON marshaling, in which case encoding/json does not walk the struct
// fields and no schema can be inferred from them.
func implementsCustomJSON(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) ||
		reflect.PointerTo(t).Implements(jsonMarshalerType) ||
		t.Implements(jsonUnmarshalerType) ||
		reflect.PointerTo(t).Implements(jsonUnmarshalerType)
}

// fieldSpec is one JSON-visible struct field mapped to a JSON Schema property.
type fieldSpec struct {
	name     string
	schema   any
	required bool
}

// schemaBuilder reflects a parameter struct into a JSON Schema node. It carries
// the tool name (used in panic messages) and the set of struct types currently
// being expanded, which detects recursive types such as []*Node.
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
			// Direct fields precede promoted embedded fields in the list, so
			// the first occurrence wins: direct over promoted, and the
			// shallower embedded field over a deeper one.
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

// fields returns the JSON-visible fields of a struct in precedence order:
// directly declared fields first, then fields promoted from anonymous embedded
// structs. fieldSpec.name is the JSON name used during encoding/json decode.
// path is the dotted field path used in panic messages.
func (b *schemaBuilder) fields(t reflect.Type, path string) []fieldSpec {
	var direct, promoted []fieldSpec

	for sf := range t.Fields() {
		// Like encoding/json, anonymous embedded struct fields (and pointers to
		// them) participate even when the embedded type name is unexported,
		// because their exported sub-fields are promoted; every other unexported
		// field is invisible to JSON.
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

		// Flatten anonymous embedded structs (and pointers to them) that have
		// no explicit json name and no custom marshaling, matching encoding/json.
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

// typeSchema maps a Go type to a JSON Schema node. path is the dotted field
// path used in panic messages so failures name the offending field.
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

// parseJSONTag extracts the JSON name and options from a field's json tag.
// skip is true for fields excluded from JSON (json:"-"). An empty name with
// skip false means the tag gave no explicit name and the Go field name applies.
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

// makeNullable marks a schema node as also accepting null, the JSON encoding of
// a nil pointer. Pointers collapse to a single null level, matching encoding/json.
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
