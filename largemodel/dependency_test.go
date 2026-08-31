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
	"slices"
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

// rootVendorImportWhitelist is the exhaustive, per-file list of root-package
// production files allowed to import an aimodel vendor package, together with
// the exact imports each may take.
//
// It is a controlled exception, not an extension point. These two files are
// the compose glue: they bind a provider's routed pool to the root package's
// backend interfaces, and the method sets they implement are spelled in vendor
// wire types, so the import cannot be avoided there. Everything else that
// depends on a vendor wire shape — request assembly, response and usage
// normalization, stream decoding, error classification, capability derivation —
// belongs to largemodel/provider/{openais,anthropics}.
//
// Adding an entry means widening the provider boundary and needs boundary
// review, so the map is keyed by exact file name with an exact import set:
// no directory rule, no prefix, no wildcard. Routing a vendor import through
// another root file, or re-exporting it indirectly, is the thing this gate
// exists to catch.
var rootVendorImportWhitelist = map[string]map[string]bool{
	"openai_compose.go":    {"github.com/vogo/aimodel/openai": true},
	"anthropic_compose.go": {"github.com/vogo/aimodel/anthropic": true},
}

// vendorImportPaths are the aimodel protocol packages the root package must not
// reach for outside the whitelist.
var vendorImportPaths = []string{
	"github.com/vogo/aimodel/openai",
	"github.com/vogo/aimodel/anthropic",
}

// TestRootPackageVendorImportsAreWhitelisted verifies the largemodel root
// package holds no vendor protocol knowledge: only the listed compose glue
// files may name an aimodel vendor package, and only the import listed for
// them.
//
// Test files are deliberately not checked. A test may drive a vendor wire
// shape end to end, and doing so says nothing about where production knowledge
// lives — nor can a test import be used to widen this whitelist.
func TestRootPackageVendorImportsAreWhitelisted(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageFiles(t, fset, ".", parser.ImportsOnly)

	for path, file := range files {
		name := filepath.Base(path)
		allowed := rootVendorImportWhitelist[name]

		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
			}

			if !isVendorImport(importPath) {
				continue
			}

			if !allowed[importPath] {
				t.Errorf("%s: root package file must not import %q; "+
					"vendor wire knowledge belongs in largemodel/provider, and the compose-glue "+
					"whitelist in this test is the only exception", name, importPath)
			}
		}
	}
}

// TestRootVendorImportWhitelistIsExact verifies every whitelisted file exists
// and still takes the vendor import it was granted, so an entry cannot outlive
// the reason it was added.
func TestRootVendorImportWhitelistIsExact(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageFiles(t, fset, ".", parser.ImportsOnly)

	byName := map[string]*ast.File{}
	for path, file := range files {
		byName[filepath.Base(path)] = file
	}

	for name, allowed := range rootVendorImportWhitelist {
		file, ok := byName[name]
		if !ok {
			t.Errorf("whitelisted file %s no longer exists; drop its entry", name)

			continue
		}

		for want := range allowed {
			found := false

			for _, imp := range file.Imports {
				importPath, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
				}

				if importPath == want {
					found = true

					break
				}
			}

			if !found {
				t.Errorf("%s no longer imports %q; drop the whitelist entry rather than keeping "+
					"a licence to import it again", name, want)
			}
		}
	}
}

func isVendorImport(path string) bool {
	return slices.Contains(vendorImportPaths, path)
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
