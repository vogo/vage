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

package vector_test

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath   = "github.com/vogo/vage"
	rootPkg      = modulePath + "/vector"
	openaisPkg   = rootPkg + "/provider/openais"
	voyagesPkg   = rootPkg + "/provider/voyages"
	embedcorePkg = rootPkg + "/internal/embedcore"
)

// productionImports collects the import paths of a package's non-test
// files. Test files are excluded on purpose: the constraints below are
// about the shipped dependency topology, and external test packages are
// allowed to reach back up into the root package.
func productionImports(t *testing.T, dir string) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	imports := map[string]bool{}
	seen := 0

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
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		seen++

		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
			}
			imports[p] = true
		}
	}

	if seen == 0 {
		t.Fatalf("no production Go files found in %s — the constraint would pass vacuously", dir)
	}

	return imports
}

// TestProvidersAreIsolated verifies the embedding providers stay peers:
// no cross-vendor imports, and no lossy shared wire layer sneaking in
// between them.
func TestProvidersAreIsolated(t *testing.T) {
	providers := map[string]string{
		openaisPkg: "provider/openais",
		voyagesPkg: "provider/voyages",
	}

	for pkg, dir := range providers {
		imports := productionImports(t, dir)

		for other := range providers {
			if other != pkg && imports[other] {
				t.Errorf("%s must not import %s — providers are peers", dir, other)
			}
		}
	}
}

// TestProvidersDoNotImportRoot guards the cycle that forced ErrEmptyQuery
// down into vector/internal/embedcore: the root package imports every
// provider, so no provider may import the root package back.
func TestProvidersDoNotImportRoot(t *testing.T) {
	for _, dir := range []string{"provider/openais", "provider/voyages"} {
		imports := productionImports(t, dir)

		if imports[rootPkg] {
			t.Errorf("%s must not import the vector root package (import cycle via NewEmbedderFromConfig)", dir)
		}
		if !imports[embedcorePkg] {
			t.Errorf("%s should take its shared sentinels from %s", dir, embedcorePkg)
		}
	}
}

// TestProvidersStayOutOfTheChatStack verifies embedding remains layered
// apart from the chat protocol stack: no largemodel, no aimodel, no
// router. This is the topology the intent's non-goals pin down.
func TestProvidersStayOutOfTheChatStack(t *testing.T) {
	forbidden := []string{
		modulePath + "/largemodel",
		"github.com/vogo/aimodel",
	}

	for _, dir := range []string{".", "provider/openais", "provider/voyages"} {
		imports := productionImports(t, dir)

		for path := range imports {
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s must not import %q — embedding stays layered apart from chat", dir, path)
				}
			}
		}
	}
}

// TestEmbedcoreStaysSelfContained keeps the shared contract point from
// growing into a second implementation layer: it must depend on nothing
// inside this module.
func TestEmbedcoreStaysSelfContained(t *testing.T) {
	for path := range productionImports(t, "internal/embedcore") {
		if strings.HasPrefix(path, modulePath) {
			t.Errorf("internal/embedcore must stay dependency-free, found %q", path)
		}
	}
}

// TestRootImportsBothProviders is the positive half: the config-driven
// constructor is only useful if it can actually reach both vendors.
func TestRootImportsBothProviders(t *testing.T) {
	imports := productionImports(t, ".")

	for _, pkg := range []string{openaisPkg, voyagesPkg} {
		if !imports[pkg] {
			t.Errorf("vector root should import %s for NewEmbedderFromConfig", pkg)
		}
	}
}
