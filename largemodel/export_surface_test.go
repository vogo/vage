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

package largemodel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestExportSurface_ResponseSchemaPromptNotExported(t *testing.T) {
	for _, name := range exportedDeclNames(t) {
		if name == "DegradeResponseSchemaPrompt" {
			t.Fatal("DegradeResponseSchemaPrompt must stay package-internal")
		}
	}
}

// TestExportSurface_VendorPrefixedConstructorsStayUnexported pins the public
// construction surface to NewCaller, BuildCaller and WrapCaller. A
// vendor-prefixed constructor added as a convenience would put a second naming
// beside them, which is the thing the generic entry points exist to avoid.
func TestExportSurface_VendorPrefixedConstructorsStayUnexported(t *testing.T) {
	unexported := map[string]bool{
		"NewOpenAIChatCallerFromConfig":          true,
		"NewOpenAIChatCallerFromEndpoint":        true,
		"NewOpenAIChatCallerFromBackend":         true,
		"NewAnthropicMessagesCallerFromConfig":   true,
		"NewAnthropicMessagesCallerFromEndpoint": true,
		"NewAnthropicMessagesCallerFromBackend":  true,
	}

	for _, name := range exportedDeclNames(t) {
		if unexported[name] {
			t.Errorf("%s must not be exported: the entry points are NewCaller, BuildCaller and WrapCaller", name)
		}
	}
}

func TestExportSurface_ProtocolOpenAIResponsesStillRejected(t *testing.T) {
	if schema.ProtocolOpenAIResponses.Valid() {
		t.Fatal("ProtocolOpenAIResponses must remain invalid until a public Caller exists")
	}

	if err := schema.ProtocolOpenAIResponses.Validate(); err == nil {
		t.Fatal("ProtocolOpenAIResponses.Validate must fail")
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

// Compile-time guard: the internal helper must remain reachable from this package.
var _ = degradeResponseSchemaPrompt
