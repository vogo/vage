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

package integrations_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vogo/vage/largemodel/middleware/contexteditor"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const modulePath = "github.com/vogo/vage"

// component describes an architecture component from doc/architecture/architecture.md.
type component struct {
	name  string
	layer int // 0=L0 … 4=L4; platform=-1
}

// componentRules maps package prefixes to components. Longer prefixes are
// checked first so subpackages stay within their parent component.
var componentRules = []struct {
	prefix    string
	component component
}{
	{modulePath + "/schema", component{"schema", 0}},
	{modulePath + "/prompt", component{"prompt", 0}},
	{modulePath + "/agent/taskagent", component{"taskagent", 3}},
	{modulePath + "/agent/routeragent", component{"routeragent", 3}},
	{modulePath + "/agent/workflowagent", component{"workflowagent", 3}},
	{modulePath + "/agent", component{"agent", 3}},
	{modulePath + "/checkpoint", component{"checkpoint", 1}},
	{modulePath + "/orchestrate", component{"orchestrate", 1}},
	{modulePath + "/workflow", component{"workflow", 1}},
	{modulePath + "/interrupt", component{"interrupt", 1}},
	{modulePath + "/memory", component{"memory", 1}},
	{modulePath + "/context", component{"context", 1}},
	{modulePath + "/session/tree/vectorhook", component{"session", 1}},
	{modulePath + "/session/tree", component{"session", 1}},
	{modulePath + "/session", component{"session", 1}},
	{modulePath + "/workspace", component{"workspace", 1}},
	{modulePath + "/sessionview", component{"sessionview", 1}},
	{modulePath + "/largemodel", component{"largemodel", 2}},
	{modulePath + "/tool", component{"tool", 2}},
	{modulePath + "/mcp", component{"mcp", 2}},
	{modulePath + "/skill", component{"skill", 2}},
	{modulePath + "/guard", component{"guard", 2}},
	{modulePath + "/security", component{"security", 2}},
	{modulePath + "/service", component{"service", 4}},
	{modulePath + "/hook", component{"hook", 4}},
	{modulePath + "/eval", component{"eval", 4}},
	{modulePath + "/vector", component{"vector", -1}},
}

// allowedCrossComponent lists composition/adapter edges that do not follow
// strict layer descent but are intentional (see architecture.md).
var allowedCrossComponent = map[string]map[string]struct{}{
	"routeragent":   {"agent": {}},
	"taskagent":     {"agent": {}, "hook": {}},
	"workflowagent": {"agent": {}},
	"context":       {"hook": {}, "memory": {}, "session": {}, "vector": {}, "workspace": {}},
	"session":       {"hook": {}, "largemodel": {}, "memory": {}, "vector": {}},
	"tool":          {"agent": {}, "session": {}, "sessionview": {}, "vector": {}, "workspace": {}},
	"mcp":           {"security": {}, "tool": {}, "agent": {}},
	"vector":        {"hook": {}},
}

// forbiddenCrossComponent encodes hard red lines; these override allowlists.
var forbiddenCrossComponent = map[string]map[string]struct{}{
	"tool":       {"memory": {}},
	"largemodel": {"tool": {}, "memory": {}},
}

// edgeExemption records a reviewed, single-edge exception with its ADR path.
type edgeExemption struct {
	fromPkg string
	toPkg   string
	adr     string
}

var edgeExemptions = []edgeExemption{}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func resolveComponent(pkg string) (component, bool) {
	for _, rule := range componentRules {
		if pkg == rule.prefix || strings.HasPrefix(pkg, rule.prefix+"/") {
			return rule.component, true
		}
	}
	return component{}, false
}

func layerLabel(layer int) string {
	switch layer {
	case 0:
		return "L0"
	case 1:
		return "L1"
	case 2:
		return "L2"
	case 3:
		return "L3"
	case 4:
		return "L4"
	case -1:
		return "platform"
	default:
		return fmt.Sprintf("layer(%d)", layer)
	}
}

// isStdlibImport reports whether path is a Go standard-library import.
// Module paths always contain a dot; stdlib paths (fmt, context, …) do not.
func isStdlibImport(path string) bool {
	return !strings.Contains(path, ".")
}

func assertL0StdlibOnly(t *testing.T, pkg string, imports map[string]bool) {
	t.Helper()

	for imp := range imports {
		if isStdlibImport(imp) {
			continue
		}
		t.Errorf("%s must only import the standard library, found %q", pkg, imp)
	}
}

func assertComponentL0StdlibOnly(t *testing.T, pkgs map[string]map[string]bool, compName string) {
	t.Helper()

	found := false
	for pkg, imports := range pkgs {
		comp, ok := resolveComponent(pkg)
		if !ok || comp.name != compName {
			continue
		}
		found = true
		assertL0StdlibOnly(t, pkg, imports)
	}
	if !found {
		t.Fatalf("no production packages mapped to %q component", compName)
	}
}

func isExempted(fromPkg, toPkg string) (string, bool) {
	for _, ex := range edgeExemptions {
		if ex.fromPkg == fromPkg && ex.toPkg == toPkg {
			return ex.adr, true
		}
	}
	return "", false
}

func isAllowedImport(fromPkg, toPkg string) (allowed bool, reason string) {
	if !strings.HasPrefix(toPkg, modulePath+"/") {
		return true, "external"
	}
	if fromPkg == toPkg {
		return true, "self"
	}

	fromComp, okFrom := resolveComponent(fromPkg)
	toComp, okTo := resolveComponent(toPkg)
	if !okFrom {
		return false, "source package is not mapped to an architecture component"
	}
	if !okTo {
		return false, "target package is not mapped to an architecture component"
	}

	if fromComp.name == toComp.name {
		return true, "same component"
	}

	if toComp.name == "schema" || toComp.name == "prompt" {
		return true, "L0 contract"
	}

	if _, forbidden := forbiddenCrossComponent[fromComp.name][toComp.name]; forbidden {
		return false, fmt.Sprintf("forbidden cross-component edge %s → %s", fromComp.name, toComp.name)
	}

	if adr, ok := isExempted(fromPkg, toPkg); ok {
		return true, "ADR exemption: " + adr
	}

	if _, ok := allowedCrossComponent[fromComp.name][toComp.name]; ok {
		return true, "allowed composition edge"
	}

	if fromComp.layer > toComp.layer && toComp.layer >= 0 {
		return true, "layer descent"
	}

	return false, fmt.Sprintf(
		"cross-component import %s (%s) → %s (%s) is not allowed",
		fromComp.name, layerLabel(fromComp.layer),
		toComp.name, layerLabel(toComp.layer),
	)
}

func productionPackages(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()

	pkgs := map[string]map[string]bool{}
	var walk func(string)
	walk = func(dir string) {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("rel %s: %v", dir, err)
		}
		if rel == "integrations" || strings.HasPrefix(rel, "integrations"+string(os.PathSeparator)) {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}

		hasProd := false
		fset := token.NewFileSet()
		imports := map[string]bool{}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			path := filepath.Join(dir, name)

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			hasProd = true

			for _, imp := range file.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("unquote import %s: %v", imp.Path.Value, err)
				}
				imports[p] = true
			}
		}

		if hasProd {
			pkgPath := modulePath
			if rel != "." {
				pkgPath = modulePath + "/" + filepath.ToSlash(rel)
			}
			pkgs[pkgPath] = imports
		}

		for _, entry := range entries {
			if entry.IsDir() {
				walk(filepath.Join(dir, entry.Name()))
			}
		}
	}

	walk(root)
	if len(pkgs) == 0 {
		t.Fatal("no production packages found — scan would pass vacuously")
	}
	return pkgs
}

func checkEdge(t *testing.T, fromPkg, toPkg string) {
	t.Helper()

	if fromPkg == toPkg {
		return
	}

	allowed, reason := isAllowedImport(fromPkg, toPkg)
	if allowed {
		return
	}

	fromComp, _ := resolveComponent(fromPkg)
	toComp, _ := resolveComponent(toPkg)
	t.Errorf(
		"illegal import:\n  from: %s (%s %s)\n  to:   %s (%s %s)\n  rule: %s",
		fromPkg, fromComp.name, layerLabel(fromComp.layer),
		toPkg, toComp.name, layerLabel(toComp.layer),
		reason,
	)
}

// TestArchitectureProductionDependencies scans every production package and
// asserts direct import edges match the rules in doc/architecture/architecture.md.
func TestArchitectureProductionDependencies(t *testing.T) {
	root := repoRoot(t)
	pkgs := productionPackages(t, root)

	for fromPkg, imports := range pkgs {
		if _, ok := resolveComponent(fromPkg); !ok {
			t.Errorf("production package %s is not mapped to an architecture component", fromPkg)
			continue
		}
		for toPkg := range imports {
			if !strings.HasPrefix(toPkg, modulePath+"/") {
				continue
			}
			checkEdge(t, fromPkg, toPkg)
		}
	}
}

func TestArchitectureSchemaIsRootContract(t *testing.T) {
	root := repoRoot(t)
	pkgs := productionPackages(t, root)

	assertComponentL0StdlibOnly(t, pkgs, "schema")
}

func TestArchitecturePromptIsRootContract(t *testing.T) {
	root := repoRoot(t)
	pkgs := productionPackages(t, root)

	assertComponentL0StdlibOnly(t, pkgs, "prompt")
}

func TestArchitectureL0SubpackageStdlibSynthetic(t *testing.T) {
	cases := []struct {
		name string
		pkg  string
		imp  string
	}{
		{
			name: "schema subpackage third-party import",
			pkg:  modulePath + "/schema/experimental",
			imp:  "github.com/vogo/aimodel",
		},
		{
			name: "prompt subpackage third-party import",
			pkg:  modulePath + "/prompt/templates",
			imp:  "github.com/some/thirdparty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp, ok := resolveComponent(tc.pkg)
			if !ok || comp.name == "" || comp.layer != 0 {
				t.Fatalf("expected %s to map to an L0 component, got %+v ok=%v", tc.pkg, comp, ok)
			}

			// Topology check skips external imports; L0 stdlib assert must still catch them.
			allowed, _ := isAllowedImport(tc.pkg, tc.imp)
			if !allowed {
				t.Fatalf("expected external import to pass topology check (L0 assert covers stdlib-only)")
			}

			imports := map[string]bool{tc.imp: true}
			if isStdlibImport(tc.imp) {
				t.Fatalf("test case %q must use a non-stdlib import", tc.imp)
			}

			failed := false
			sub := &testing.T{}
			assertL0StdlibOnly(sub, tc.pkg, imports)
			if sub.Failed() {
				failed = true
			}
			if !failed {
				t.Fatal("expected L0 stdlib assertion to reject subpackage third-party import")
			}
		})
	}
}

func TestArchitectureSyntheticViolations(t *testing.T) {
	cases := []struct {
		name    string
		fromPkg string
		toPkg   string
	}{
		{name: "tool must not import memory", fromPkg: modulePath + "/tool", toPkg: modulePath + "/memory"},
		{name: "largemodel must not import tool", fromPkg: modulePath + "/largemodel", toPkg: modulePath + "/tool"},
		{name: "largemodel must not import memory", fromPkg: modulePath + "/largemodel", toPkg: modulePath + "/memory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, _ := isAllowedImport(tc.fromPkg, tc.toPkg)
			if allowed {
				t.Fatalf("expected %s → %s to be rejected", tc.fromPkg, tc.toPkg)
			}
		})
	}
}

func TestArchitectureSyntheticAllowances(t *testing.T) {
	cases := []struct {
		name    string
		fromPkg string
		toPkg   string
	}{
		{name: "taskagent integrates tool", fromPkg: modulePath + "/agent/taskagent", toPkg: modulePath + "/tool"},
		{name: "context reaches vector", fromPkg: modulePath + "/context", toPkg: modulePath + "/vector"},
		{name: "same component subpackage", fromPkg: modulePath + "/tool/read", toPkg: modulePath + "/tool"},
		{name: "L2 descends to schema", fromPkg: modulePath + "/largemodel", toPkg: modulePath + "/schema"},
		{name: "typed workflow descends to schema", fromPkg: modulePath + "/workflow", toPkg: modulePath + "/schema"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, reason := isAllowedImport(tc.fromPkg, tc.toPkg)
			if !allowed {
				t.Fatalf("expected %s → %s to be allowed, got: %s", tc.fromPkg, tc.toPkg, reason)
			}
		})
	}
}

func TestArchitectureUnmappedPackageRejected(t *testing.T) {
	allowed, reason := isAllowedImport(modulePath+"/unknownpkg", modulePath+"/schema")
	if allowed {
		t.Fatal("unmapped source package should be rejected")
	}
	if !strings.Contains(reason, "not mapped") {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

// TestResourceLookupFuncAcceptsToolAlias verifies tool.ResourceTracker remains
// identical to schema.ResourceTracker for WithStaleResourceTracker wiring.
func TestResourceLookupFuncAcceptsToolAlias(t *testing.T) {
	var lookup contexteditor.ResourceLookupFunc = func(toolName string) tool.ResourceTracker {
		switch toolName {
		case "read":
			return stubReadTracker{}
		default:
			return nil
		}
	}

	_ = contexteditor.NewContextEditorMiddleware(
		contexteditor.WithStaleResourceTracker(lookup),
	)
}

func TestResourceLookupFuncAcceptsSchemaType(t *testing.T) {
	var lookup contexteditor.ResourceLookupFunc = func(toolName string) schema.ResourceTracker {
		switch toolName {
		case "read":
			return stubReadTracker{}
		default:
			return nil
		}
	}

	_ = contexteditor.NewContextEditorMiddleware(
		contexteditor.WithStaleResourceTracker(lookup),
	)
}

type stubReadTracker struct{}

func (stubReadTracker) ResourceIDs(args map[string]any) []schema.ResourceRef {
	p, _ := args["file_path"].(string)
	if p == "" {
		return nil
	}
	return []schema.ResourceRef{{ID: p, Mode: schema.ResourceRead}}
}

var (
	_ tool.ResourceTracker   = stubReadTracker{}
	_ schema.ResourceTracker = stubReadTracker{}
)
