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

package openais

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestExportSurface_ResponsesRouteNotPublic(t *testing.T) {
	for _, name := range exportedDeclNames(t) {
		switch name {
		case "Responder", "CapabilityResponses":
			t.Fatalf("%s must stay package-internal", name)
		}
	}

	typ := reflect.TypeFor[ComposeClient]()
	for method := range typ.Methods() {
		switch method.Name {
		case "Responses", "ResponsesStream":
			t.Fatalf("ComposeClient.%s must not be exported", method.Name)
		}
	}
}

func TestExportSurface_CapabilityHasNoMaxContextTokens(t *testing.T) {
	typ := reflect.TypeFor[Capability]()
	for field := range typ.Fields() {
		if field.Name == "MaxContextTokens" {
			t.Fatal("Capability.MaxContextTokens must not be exported")
		}
	}
}

func exportedDeclNames(t *testing.T) []string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	fset := token.NewFileSet()
	var names []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() {
					names = append(names, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							names = append(names, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() {
								names = append(names, name.Name)
							}
						}
					}
				}
			}
		}
	}

	return names
}
