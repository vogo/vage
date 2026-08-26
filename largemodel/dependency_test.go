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

package largemodel_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func parsePackageFiles(t *testing.T, fset *token.FileSet, dir string, mode parser.Mode) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		matches, err := build.Default.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("match build constraints for %s: %v", filepath.Join(dir, name), err)
		}
		if !matches {
			continue
		}

		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, mode)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = file
	}

	return files
}

func packageImports(t *testing.T, dir string) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	files := parsePackageFiles(t, fset, dir, parser.ImportsOnly)

	imports := map[string]bool{}

	for _, file := range files {
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
			}

			imports[path] = true
		}
	}

	return imports
}

func hasProviderImport(imports map[string]bool, want string) bool {
	for path := range imports {
		if path == "github.com/vogo/aimodel/"+want {
			return true
		}
	}

	return false
}

// TestRouterCoreImportsNoProvider verifies the routing core is protocol-neutral.
func TestRouterCoreImportsNoProvider(t *testing.T) {
	imports := packageImports(t, "router")

	for path := range imports {
		if strings.HasPrefix(path, "github.com/vogo/aimodel") {
			t.Errorf("router must import nothing from aimodel, found %q", path)
		}
	}
}

// TestBackendWrappersAreIsolated verifies OpenAI and Anthropic backend wrappers
// stay independent of each other.
func TestBackendWrappersAreIsolated(t *testing.T) {
	wrappers := map[string]struct{ own, forbidden string }{
		"provider/openais":    {own: "openai", forbidden: "anthropic"},
		"provider/anthropics": {own: "anthropic", forbidden: "openai"},
	}

	for dir, want := range wrappers {
		imports := packageImports(t, dir)

		if !hasProviderImport(imports, want.own) {
			t.Errorf("%s should import aimodel/%s", dir, want.own)
		}

		if hasProviderImport(imports, want.forbidden) {
			t.Errorf("%s must not import aimodel/%s", dir, want.forbidden)
		}

		if !imports["github.com/vogo/vage/largemodel/router"] {
			t.Errorf("%s should build on largemodel/router", dir)
		}

		if imports["github.com/vogo/vage/largemodel"] {
			t.Errorf("%s must stay below the largemodel facade and must not import it", dir)
		}

		for other := range wrappers {
			if other != dir && imports["github.com/vogo/vage/largemodel/"+other] {
				t.Errorf("%s must not import %s", dir, other)
			}
		}
	}
}

// TestRouterCoreExportsNoProviderType verifies the router's public API carries
// no provider request or response type.
func TestRouterCoreExportsNoProviderType(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageFiles(t, fset, "router", parser.SkipObjectResolution)

	for path, file := range files {
		moduleAliases := map[string]string{}

		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
			}

			if !strings.HasPrefix(importPath, "github.com/vogo/aimodel") {
				continue
			}

			name := importPath[strings.LastIndex(importPath, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}

			moduleAliases[name] = importPath
		}

		if len(moduleAliases) == 0 {
			continue
		}

		for _, decl := range file.Decls {
			if _, exported := exportedDeclName(decl); !exported {
				continue
			}

			ast.Inspect(decl, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}

				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}

				if importPath, found := moduleAliases[ident.Name]; found {
					t.Errorf("%s: exported API references %s.%s from %q",
						path, ident.Name, sel.Sel.Name, importPath)
				}

				return true
			})
		}
	}
}

func exportedDeclName(decl ast.Decl) (string, bool) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Name.IsExported() {
			return d.Name.Name, true
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() {
					return s.Name.Name, true
				}
			case *ast.ValueSpec:
				for _, name := range s.Names {
					if name.IsExported() {
						return name.Name, true
					}
				}
			}
		}
	}

	return "", false
}
