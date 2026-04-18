//go:build !windows

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

// Package pathguard_tests hosts the cross-cutting integration matrix for the
// path-guard feature (design §5.4). It exercises the 6 tools
// (read/write/edit/glob/grep/bash) against 4 escape vectors and 4 legal
// scenarios each — 24 rejection + 24 legal cases — to prove that the path
// guard blocks every escape while introducing no regression on valid paths.
package pathguard_tests //nolint:revive // integration test package

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/bash"
	"github.com/vogo/vage/tool/edit"
	"github.com/vogo/vage/tool/glob"
	"github.com/vogo/vage/tool/grep"
	"github.com/vogo/vage/tool/read"
	"github.com/vogo/vage/tool/toolkit"
	"github.com/vogo/vage/tool/write"
)

// ---------- shared fixtures ----------

// pathguardFixture bundles the canonical working dir, a symlink that escapes
// to an outside dir, and an outside file that the guard must never expose.
type pathguardFixture struct {
	allowedDir    string // canonical (EvalSymlinks) of the allow-listed directory
	outsideDir    string // canonical path to a directory outside the allow-list
	insideFile    string // existing regular file inside allowedDir
	insideTarget  string // existing regular file used as an edit/read target
	escapeSymlink string // symlink inside allowedDir pointing at outsideDir
	guard         *toolkit.PathGuard
}

// newFixture builds a workspace with:
//   - an allow-listed dir containing an inside file, an edit target, and a
//     symlink that points outside the allow-list;
//   - a separate outside dir with a "secret" file;
//   - a PathGuard scoped to allowedDir only.
func newFixture(t *testing.T) *pathguardFixture {
	t.Helper()

	allowed := toolkit.ResolveDir(t, t.TempDir())
	outside := toolkit.ResolveDir(t, t.TempDir())

	insideFile := toolkit.WriteTestFile(t, allowed, "inside.txt", "inside-content")
	editTarget := toolkit.WriteTestFile(t, allowed, "target.txt", "hello world")

	// Outside sensitive file used as symlink destination / absolute-path target.
	toolkit.WriteTestFile(t, outside, "secret.txt", "top-secret")

	escape := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	guard, err := toolkit.NewPathGuard([]string{allowed})
	if err != nil {
		t.Fatalf("NewPathGuard failed: %v", err)
	}

	t.Cleanup(func() { _ = guard.Close() })

	return &pathguardFixture{
		allowedDir:    allowed,
		outsideDir:    outside,
		insideFile:    insideFile,
		insideTarget:  editTarget,
		escapeSymlink: escape,
		guard:         guard,
	}
}

func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}

	return ""
}

// mustJSON marshals v to JSON string for passing as tool args.
func mustJSON(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	return string(b)
}

// assertRejected runs a tool via the registry and fails the test unless the
// call returns IsError=true. It also checks that the caller-supplied substring
// appears in the error message — the design requires error text to be
// self-descriptive (§3) so that LLM callers can self-correct.
func assertRejected(t *testing.T, reg *tool.Registry, name, args, wantSubstr, scenario string) {
	t.Helper()

	res, err := reg.Execute(context.Background(), name, args)
	if err != nil {
		t.Fatalf("[%s] registry.Execute returned unexpected error: %v", scenario, err)
	}

	if !res.IsError {
		t.Fatalf("[%s] expected IsError=true, got success with text: %q", scenario, resultText(res))
	}

	if wantSubstr != "" && !strings.Contains(resultText(res), wantSubstr) {
		t.Errorf("[%s] error message %q does not contain %q", scenario, resultText(res), wantSubstr)
	}
}

// assertAccepted runs a tool and fails the test if IsError=true. Some tools
// produce output the caller may want to inspect; optionalCheck runs when the
// execution succeeds. Legal-path cases are about "no regression": the guard
// must not reject valid work.
func assertAccepted(t *testing.T, reg *tool.Registry, name, args, scenario string, optionalCheck func(text string)) {
	t.Helper()

	res, err := reg.Execute(context.Background(), name, args)
	if err != nil {
		t.Fatalf("[%s] registry.Execute returned unexpected error: %v", scenario, err)
	}

	if res.IsError {
		t.Fatalf("[%s] expected success, got error: %s", scenario, resultText(res))
	}

	if optionalCheck != nil {
		optionalCheck(resultText(res))
	}
}

// registerRead returns a registry with the read tool installed under the
// shared PathGuard.
func registerRead(t *testing.T, fx *pathguardFixture) *tool.Registry {
	t.Helper()

	reg := tool.NewRegistry()
	if err := read.Register(reg, read.WithPathGuard(fx.guard)); err != nil {
		t.Fatalf("read.Register: %v", err)
	}

	return reg
}

func registerWrite(t *testing.T, fx *pathguardFixture) *tool.Registry {
	t.Helper()

	reg := tool.NewRegistry()
	if err := write.Register(reg, write.WithPathGuard(fx.guard)); err != nil {
		t.Fatalf("write.Register: %v", err)
	}

	return reg
}

// registerEdit installs the edit tool without a ReadTracker — the path guard
// is the only gate under test here. The edit requires the file to pre-exist.
func registerEdit(t *testing.T, fx *pathguardFixture) *tool.Registry {
	t.Helper()

	reg := tool.NewRegistry()
	if err := edit.Register(reg, edit.WithPathGuard(fx.guard)); err != nil {
		t.Fatalf("edit.Register: %v", err)
	}

	return reg
}

func registerGlob(t *testing.T, fx *pathguardFixture) *tool.Registry {
	t.Helper()

	reg := tool.NewRegistry()
	if err := glob.Register(reg, glob.WithPathGuard(fx.guard), glob.WithWorkingDir(fx.allowedDir)); err != nil {
		t.Fatalf("glob.Register: %v", err)
	}

	return reg
}

func registerGrep(t *testing.T, fx *pathguardFixture) *tool.Registry {
	t.Helper()

	reg := tool.NewRegistry()
	if err := grep.Register(reg, grep.WithPathGuard(fx.guard), grep.WithWorkingDir(fx.allowedDir)); err != nil {
		t.Fatalf("grep.Register: %v", err)
	}

	return reg
}

// registerBash installs the bash tool with a guardian scoped to the allow-list.
// The bash tool only hard-blocks TierBlocked; TierDangerous classifications
// are asserted by calling guardian.Classify directly since PermissionState is
// not in this wiring.
func registerBash(t *testing.T, fx *pathguardFixture) (*tool.Registry, *bash.PathGuardian) {
	t.Helper()

	guardian := bash.NewPathGuardian([]string{fx.allowedDir}, fx.allowedDir)

	reg := tool.NewRegistry()
	if err := bash.Register(reg,
		bash.WithPathGuardian(guardian),
		bash.WithWorkingDir(fx.allowedDir),
	); err != nil {
		t.Fatalf("bash.Register: %v", err)
	}

	return reg, guardian
}

// ---------- READ tool ----------

// TestRead_Reject covers the 4 rejection vectors on the read tool.
func TestRead_Reject(t *testing.T) {
	fx := newFixture(t)
	reg := registerRead(t, fx)

	// Vector 1: literal ".." escapes the allow-list after filepath.Clean.
	// Scenario: malicious model passes <allowed>/../<outside>/secret.txt and
	// expects the cleaned absolute form to be rejected.
	t.Run("literal_dotdot", func(t *testing.T) {
		outsideName := filepath.Base(fx.outsideDir)
		escapePath := filepath.Join(fx.allowedDir, "..", outsideName, "secret.txt")
		assertRejected(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": escapePath}),
			"path not allowed",
			"read/literal_dotdot",
		)
	})

	// Vector 2: an absolute path to an unrelated sensitive file (/etc/passwd).
	// This is the canonical "direct exfiltration" case in requirement US-3.
	t.Run("absolute_outside", func(t *testing.T) {
		assertRejected(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": "/etc/passwd"}),
			"path not allowed",
			"read/absolute_outside",
		)
	})

	// Vector 3: a symlink created inside the allow-list that points at an
	// outside directory; accessing a child through the symlink must be
	// rejected (os.Root refuses to traverse out-of-root symlinks atomically).
	t.Run("symlink_outside", func(t *testing.T) {
		assertRejected(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": filepath.Join(fx.escapeSymlink, "secret.txt")}),
			"read tool",
			"read/symlink_outside",
		)
	})

	// Vector 4 (tool-specific for read): empty path. Guard's surface check
	// rejects empty input before any filesystem call.
	t.Run("empty_path", func(t *testing.T) {
		assertRejected(t, reg, "read",
			`{"file_path":""}`,
			"must not be empty",
			"read/empty_path",
		)
	})
}

// TestRead_Legal covers 4 zero-regression scenarios for read.
func TestRead_Legal(t *testing.T) {
	fx := newFixture(t)
	reg := registerRead(t, fx)

	// Legal 1: reading a file by its canonical absolute path inside the
	// allow-list. The baseline success path.
	t.Run("absolute_inside", func(t *testing.T) {
		assertAccepted(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": fx.insideFile}),
			"read/absolute_inside",
			func(text string) {
				if text != "inside-content" {
					t.Errorf("expected %q, got %q", "inside-content", text)
				}
			},
		)
	})

	// Legal 2: reading the allow-listed directory itself produces a listing.
	// Verifies directory-mode read still works under a guard.
	t.Run("directory_listing", func(t *testing.T) {
		assertAccepted(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": fx.allowedDir}),
			"read/directory_listing",
			func(text string) {
				if !strings.Contains(text, "Directory:") {
					t.Errorf("expected Directory: header, got %q", text)
				}
			},
		)
	})

	// Legal 3: reading with offset/limit inside the allow-list. Confirms that
	// parameter plumbing survives the guard.
	t.Run("offset_limit", func(t *testing.T) {
		multiline := toolkit.WriteTestFile(t, fx.allowedDir, "multiline.txt",
			"line1\nline2\nline3\nline4")
		assertAccepted(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": multiline, "offset": 2, "limit": 2}),
			"read/offset_limit",
			func(text string) {
				if text != "line2\nline3" {
					t.Errorf("expected line2/line3, got %q", text)
				}
			},
		)
	})

	// Legal 4: reading a file that lives inside a nested subdirectory of the
	// allow-list (containment — not just equal-to — must be accepted).
	t.Run("nested_inside", func(t *testing.T) {
		nestedDir := filepath.Join(fx.allowedDir, "sub", "deep")
		if err := os.MkdirAll(nestedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		nested := toolkit.WriteTestFile(t, nestedDir, "buried.txt", "buried")
		assertAccepted(t, reg, "read",
			mustJSON(t, map[string]any{"file_path": nested}),
			"read/nested_inside",
			func(text string) {
				if text != "buried" {
					t.Errorf("expected 'buried', got %q", text)
				}
			},
		)
	})
}

// ---------- WRITE tool ----------

// TestWrite_Reject covers 4 rejection vectors on the write tool.
func TestWrite_Reject(t *testing.T) {
	fx := newFixture(t)
	reg := registerWrite(t, fx)

	// Vector 1: literal ".." escape in file_path, aiming to write outside
	// the allow-list.
	t.Run("literal_dotdot", func(t *testing.T) {
		outsideName := filepath.Base(fx.outsideDir)
		escape := filepath.Join(fx.allowedDir, "..", outsideName, "planted.txt")
		assertRejected(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": escape, "content": "x"}),
			"path not allowed",
			"write/literal_dotdot",
		)
	})

	// Vector 2: absolute path outside the allow-list — attempt to overwrite
	// /etc/hosts is the classic destructive request.
	t.Run("absolute_outside", func(t *testing.T) {
		assertRejected(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": "/etc/hosts", "content": "127.0.0.1 bad"}),
			"path not allowed",
			"write/absolute_outside",
		)
	})

	// Vector 3: write through an inside-symlink that points to an outside
	// dir. Even though the starting path is under the allow-list, the symlink
	// resolves outside and must be rejected.
	t.Run("symlink_outside", func(t *testing.T) {
		target := filepath.Join(fx.escapeSymlink, "planted.txt")
		assertRejected(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": target, "content": "x"}),
			"write tool",
			"write/symlink_outside",
		)
	})

	// Vector 4 (tool-specific): empty file_path — surface-check rejection.
	t.Run("empty_path", func(t *testing.T) {
		assertRejected(t, reg, "write",
			`{"file_path":"","content":"x"}`,
			"must not be empty",
			"write/empty_path",
		)
	})
}

// TestWrite_Legal covers 4 zero-regression scenarios for write.
func TestWrite_Legal(t *testing.T) {
	fx := newFixture(t)
	reg := registerWrite(t, fx)

	// Legal 1: creating a brand-new file inside the allow-list succeeds and
	// the file's contents match.
	t.Run("create_new_inside", func(t *testing.T) {
		target := filepath.Join(fx.allowedDir, "new.txt")
		assertAccepted(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": target, "content": "hello"}),
			"write/create_new_inside", nil)
		if data, err := os.ReadFile(target); err != nil || string(data) != "hello" {
			t.Errorf("content mismatch: err=%v data=%q", err, data)
		}
	})

	// Legal 2: overwriting an existing file inside the allow-list (create_only
	// defaults to false).
	t.Run("overwrite_existing", func(t *testing.T) {
		assertAccepted(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": fx.insideFile, "content": "replaced"}),
			"write/overwrite_existing", nil)
		if data, err := os.ReadFile(fx.insideFile); err != nil || string(data) != "replaced" {
			t.Errorf("content mismatch: err=%v data=%q", err, data)
		}
	})

	// Legal 3: writing to a nested subdirectory — MkdirAll through the guard
	// must create missing parents inside the root.
	t.Run("nested_create", func(t *testing.T) {
		nested := filepath.Join(fx.allowedDir, "a", "b", "c", "file.txt")
		assertAccepted(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": nested, "content": "nested"}),
			"write/nested_create", nil)
		if data, err := os.ReadFile(nested); err != nil || string(data) != "nested" {
			t.Errorf("content mismatch: err=%v data=%q", err, data)
		}
	})

	// Legal 4: create_only=true succeeds on a path that does not exist yet.
	t.Run("create_only_new", func(t *testing.T) {
		target := filepath.Join(fx.allowedDir, "only.txt")
		assertAccepted(t, reg, "write",
			mustJSON(t, map[string]any{"file_path": target, "content": "once", "create_only": true}),
			"write/create_only_new", nil)
	})
}

// ---------- EDIT tool ----------

// TestEdit_Reject covers 4 rejection vectors on the edit tool.
func TestEdit_Reject(t *testing.T) {
	fx := newFixture(t)
	reg := registerEdit(t, fx)

	// Seed an outside file so a ".." or symlink escape would find real content
	// if it weren't blocked.
	outsideTarget := toolkit.WriteTestFile(t, fx.outsideDir, "editable.txt", "before")

	// Vector 1: literal ".." escape.
	t.Run("literal_dotdot", func(t *testing.T) {
		outsideName := filepath.Base(fx.outsideDir)
		escape := filepath.Join(fx.allowedDir, "..", outsideName, "editable.txt")
		assertRejected(t, reg, "edit",
			mustJSON(t, map[string]any{"file_path": escape, "old_string": "before", "new_string": "after"}),
			"path not allowed",
			"edit/literal_dotdot",
		)
	})

	// Vector 2: absolute outside path — attempt to rewrite a real file
	// outside the allow-list.
	t.Run("absolute_outside", func(t *testing.T) {
		assertRejected(t, reg, "edit",
			mustJSON(t, map[string]any{"file_path": outsideTarget, "old_string": "before", "new_string": "after"}),
			"path not allowed",
			"edit/absolute_outside",
		)
	})

	// Vector 3: symlink escape — path starts inside the allow-list but
	// traverses a symlink to the outside editable file.
	t.Run("symlink_outside", func(t *testing.T) {
		target := filepath.Join(fx.escapeSymlink, "editable.txt")
		assertRejected(t, reg, "edit",
			mustJSON(t, map[string]any{"file_path": target, "old_string": "before", "new_string": "after"}),
			"edit tool",
			"edit/symlink_outside",
		)
	})

	// Vector 4 (tool-specific): empty file_path — surface-check rejection.
	t.Run("empty_path", func(t *testing.T) {
		assertRejected(t, reg, "edit",
			`{"file_path":"","old_string":"a","new_string":"b"}`,
			"must not be empty",
			"edit/empty_path",
		)
	})
}

// TestEdit_Legal covers 4 zero-regression scenarios for edit.
func TestEdit_Legal(t *testing.T) {
	fx := newFixture(t)
	reg := registerEdit(t, fx)

	// Legal 1: simple single-occurrence replacement on the insideTarget file.
	t.Run("single_replacement", func(t *testing.T) {
		assertAccepted(t, reg, "edit",
			mustJSON(t, map[string]any{
				"file_path":  fx.insideTarget,
				"old_string": "hello",
				"new_string": "HELLO",
			}),
			"edit/single_replacement", nil)
		data, _ := os.ReadFile(fx.insideTarget)
		if string(data) != "HELLO world" {
			t.Errorf("expected 'HELLO world', got %q", string(data))
		}
	})

	// Legal 2: replace_all=true across multiple occurrences.
	t.Run("replace_all", func(t *testing.T) {
		f := toolkit.WriteTestFile(t, fx.allowedDir, "many.txt", "foo foo foo")
		assertAccepted(t, reg, "edit",
			mustJSON(t, map[string]any{
				"file_path":   f,
				"old_string":  "foo",
				"new_string":  "bar",
				"replace_all": true,
			}),
			"edit/replace_all", nil)
		data, _ := os.ReadFile(f)
		if string(data) != "bar bar bar" {
			t.Errorf("expected 'bar bar bar', got %q", string(data))
		}
	})

	// Legal 3: edit a file in a nested subdirectory of the allow-list.
	t.Run("nested_inside", func(t *testing.T) {
		dir := filepath.Join(fx.allowedDir, "pkg")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := toolkit.WriteTestFile(t, dir, "nested.go", "package pkg")
		assertAccepted(t, reg, "edit",
			mustJSON(t, map[string]any{
				"file_path":  f,
				"old_string": "pkg",
				"new_string": "main",
			}),
			"edit/nested_inside", nil)
		data, _ := os.ReadFile(f)
		if string(data) != "package main" {
			t.Errorf("expected 'package main', got %q", string(data))
		}
	})

	// Legal 4: empty new_string acts as a delete of the matched text.
	t.Run("delete_match", func(t *testing.T) {
		f := toolkit.WriteTestFile(t, fx.allowedDir, "del.txt", "keep-DROP-keep")
		assertAccepted(t, reg, "edit",
			mustJSON(t, map[string]any{
				"file_path":  f,
				"old_string": "DROP",
				"new_string": "",
			}),
			"edit/delete_match", nil)
		data, _ := os.ReadFile(f)
		if string(data) != "keep--keep" {
			t.Errorf("expected 'keep--keep', got %q", string(data))
		}
	})
}

// ---------- GLOB tool ----------

// TestGlob_Reject covers 4 rejection vectors on glob.
func TestGlob_Reject(t *testing.T) {
	fx := newFixture(t)
	reg := registerGlob(t, fx)

	// Vector 1: literal ".." in the search path. After filepath.Clean the
	// cleaned dir sits outside the allow-list.
	t.Run("literal_dotdot", func(t *testing.T) {
		outsideName := filepath.Base(fx.outsideDir)
		escape := filepath.Join(fx.allowedDir, "..", outsideName)
		assertRejected(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "*.txt", "path": escape}),
			"path not allowed",
			"glob/literal_dotdot",
		)
	})

	// Vector 2: absolute outside path (/etc).
	t.Run("absolute_outside", func(t *testing.T) {
		assertRejected(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "*.conf", "path": "/etc"}),
			"path not allowed",
			"glob/absolute_outside",
		)
	})

	// Vector 3: search path traverses an in-allowlist symlink that points
	// outside. The guard's Check resolves to an outside path and rejects.
	t.Run("symlink_outside", func(t *testing.T) {
		assertRejected(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "*.txt", "path": fx.escapeSymlink}),
			"symlink resolves outside",
			"glob/symlink_outside",
		)
	})

	// Vector 4 (tool-specific): pattern containing ".." components. Glob
	// blocks this independently of the path guard to prevent escapes via
	// the pattern itself.
	t.Run("dotdot_pattern", func(t *testing.T) {
		assertRejected(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "../**/*.txt", "path": fx.allowedDir}),
			"must not contain '..'",
			"glob/dotdot_pattern",
		)
	})
}

// TestGlob_Legal covers 4 zero-regression scenarios for glob.
func TestGlob_Legal(t *testing.T) {
	fx := newFixture(t)
	reg := registerGlob(t, fx)

	// Seed a small tree so pattern matches produce output.
	subdir := filepath.Join(fx.allowedDir, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolkit.WriteTestFile(t, subdir, "alpha.go", "package src")
	toolkit.WriteTestFile(t, subdir, "beta.go", "package src")

	// Legal 1: glob at the allow-listed root with a simple "*".
	t.Run("root_star", func(t *testing.T) {
		assertAccepted(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "*.txt", "path": fx.allowedDir}),
			"glob/root_star",
			func(text string) {
				if !strings.Contains(text, "inside.txt") && !strings.Contains(text, "target.txt") {
					t.Errorf("expected inside.txt or target.txt in output, got %q", text)
				}
			},
		)
	})

	// Legal 2: recursive glob (**/*.go) inside a nested subdirectory of the
	// allow-list.
	t.Run("recursive_pattern", func(t *testing.T) {
		assertAccepted(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "**/*.go", "path": fx.allowedDir}),
			"glob/recursive_pattern",
			func(text string) {
				if !strings.Contains(text, "alpha.go") || !strings.Contains(text, "beta.go") {
					t.Errorf("expected alpha.go and beta.go, got %q", text)
				}
			},
		)
	})

	// Legal 3: default-working-dir behavior — omit "path" and rely on
	// WithWorkingDir(fx.allowedDir).
	t.Run("default_workdir", func(t *testing.T) {
		assertAccepted(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "*.txt"}),
			"glob/default_workdir", nil)
	})

	// Legal 4: glob with a specific prefix pattern inside a subdirectory.
	t.Run("prefix_pattern", func(t *testing.T) {
		assertAccepted(t, reg, "glob",
			mustJSON(t, map[string]any{"pattern": "src/alpha*", "path": fx.allowedDir}),
			"glob/prefix_pattern",
			func(text string) {
				if !strings.Contains(text, "alpha.go") {
					t.Errorf("expected alpha.go, got %q", text)
				}
			},
		)
	})
}

// ---------- GREP tool ----------

// TestGrep_Reject covers 4 rejection vectors on grep.
func TestGrep_Reject(t *testing.T) {
	fx := newFixture(t)
	reg := registerGrep(t, fx)

	// Vector 1: literal ".." escape in search path.
	t.Run("literal_dotdot", func(t *testing.T) {
		outsideName := filepath.Base(fx.outsideDir)
		escape := filepath.Join(fx.allowedDir, "..", outsideName)
		assertRejected(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "secret", "path": escape}),
			"path not allowed",
			"grep/literal_dotdot",
		)
	})

	// Vector 2: absolute path outside the allow-list (/etc).
	t.Run("absolute_outside", func(t *testing.T) {
		assertRejected(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "root", "path": "/etc"}),
			"path not allowed",
			"grep/absolute_outside",
		)
	})

	// Vector 3: search via a symlink that escapes the allow-list.
	t.Run("symlink_outside", func(t *testing.T) {
		assertRejected(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "secret", "path": fx.escapeSymlink}),
			"symlink resolves outside",
			"grep/symlink_outside",
		)
	})

	// Vector 4 (tool-specific): empty path — when no path is provided and the
	// workingDir default is also empty, grep must refuse the call.
	t.Run("empty_path_no_workdir", func(t *testing.T) {
		reg2 := tool.NewRegistry()
		if err := grep.Register(reg2, grep.WithPathGuard(fx.guard)); err != nil {
			t.Fatalf("grep.Register: %v", err)
		}
		assertRejected(t, reg2, "grep",
			mustJSON(t, map[string]any{"pattern": "whatever"}),
			"no search path provided",
			"grep/empty_path_no_workdir",
		)
	})
}

// TestGrep_Legal covers 4 zero-regression scenarios for grep.
func TestGrep_Legal(t *testing.T) {
	fx := newFixture(t)
	reg := registerGrep(t, fx)

	toolkit.WriteTestFile(t, fx.allowedDir, "greppable.txt", "needle one\nother\nneedle two")

	// Legal 1: pattern found in the allow-listed dir.
	t.Run("match_in_root", func(t *testing.T) {
		assertAccepted(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "needle", "path": fx.allowedDir}),
			"grep/match_in_root",
			func(text string) {
				if !strings.Contains(text, "needle") {
					t.Errorf("expected 'needle' in output, got %q", text)
				}
			},
		)
	})

	// Legal 2: pattern with no matches returns a clean "No matches found."
	// message, not an error.
	t.Run("no_match_is_success", func(t *testing.T) {
		assertAccepted(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "definitely_not_present_xyz", "path": fx.allowedDir}),
			"grep/no_match_is_success",
			func(text string) {
				if !strings.Contains(text, "No matches") {
					t.Errorf("expected 'No matches' sentinel, got %q", text)
				}
			},
		)
	})

	// Legal 3: default-workdir path resolution — omit "path".
	t.Run("default_workdir", func(t *testing.T) {
		assertAccepted(t, reg, "grep",
			mustJSON(t, map[string]any{"pattern": "needle"}),
			"grep/default_workdir", nil)
	})

	// Legal 4: include filter restricts the files searched.
	t.Run("include_filter", func(t *testing.T) {
		assertAccepted(t, reg, "grep",
			mustJSON(t, map[string]any{
				"pattern": "needle",
				"path":    fx.allowedDir,
				"include": "*.txt",
			}),
			"grep/include_filter", nil)
	})
}

// ---------- BASH tool ----------

// TestBash_Reject covers 4 escape-vector scenarios for the bash tool. Per
// design §5.4 only TierBlocked classifications are hard-blocked by the tool
// itself; TierDangerous is asserted by calling guardian.Classify directly
// because PermissionState (the Dangerous enforcer) is not in this wiring.
func TestBash_Reject(t *testing.T) {
	fx := newFixture(t)
	reg, guardian := registerBash(t, fx)

	// Vector 1: literal ".." segment in an argument — classified Dangerous
	// by rule "path-traversal-dots". Not hard-blocked by BashTool so verify
	// via guardian.Classify directly.
	t.Run("literal_dotdot_dangerous", func(t *testing.T) {
		cls := guardian.Classify("ls ../../etc")
		if cls.Rule != "path-traversal-dots" {
			t.Errorf("expected rule path-traversal-dots, got %q (tier=%s)", cls.Rule, cls.Tier)
		}
	})

	// Vector 2: absolute path argument outside the allow-list — classified
	// TierBlocked by rule "path-outside-allowed"; BashTool hard-blocks.
	t.Run("absolute_outside_blocked", func(t *testing.T) {
		assertRejected(t, reg, "bash",
			mustJSON(t, map[string]any{"command": "cat /etc/passwd"}),
			"path-outside-allowed",
			"bash/absolute_outside_blocked",
		)
	})

	// Vector 3 (bash analogue of symlink-outside): cd to an absolute outside
	// path is classified TierBlocked "cd-outside-allowed". This is the
	// closest per-design-row equivalent of the other tools' symlink vector.
	t.Run("cd_outside_blocked", func(t *testing.T) {
		assertRejected(t, reg, "bash",
			mustJSON(t, map[string]any{"command": "cd /etc"}),
			"cd-outside-allowed",
			"bash/cd_outside_blocked",
		)
	})

	// Vector 4: command substitution — `echo $(cat /etc/passwd)` embeds an
	// absolute outside path inside $(...). The guardian extracts the inner
	// sub-command and the /etc/passwd token is TierBlocked, so BashTool
	// hard-blocks.
	t.Run("command_substitution_blocked", func(t *testing.T) {
		assertRejected(t, reg, "bash",
			mustJSON(t, map[string]any{"command": "echo $(cat /etc/passwd)"}),
			"path-outside-allowed",
			"bash/command_substitution_blocked",
		)
	})
}

// TestBash_Legal covers 4 zero-regression scenarios for the bash tool. These
// must run to completion — the guardian must not hard-block normal workflow
// commands inside the allow-list.
func TestBash_Legal(t *testing.T) {
	fx := newFixture(t)
	reg, guardian := registerBash(t, fx)

	// Pre-populate an inside file to read with bash tools.
	toolkit.WriteTestFile(t, fx.allowedDir, "hello.sh.txt", "hello via bash")

	// Legal 1: a plain echo with no path arguments — TierCaution, passes
	// the hard-block gate and executes.
	t.Run("plain_echo", func(t *testing.T) {
		assertAccepted(t, reg, "bash",
			mustJSON(t, map[string]any{"command": "echo hello"}),
			"bash/plain_echo",
			func(text string) {
				if !strings.Contains(text, "hello") {
					t.Errorf("expected 'hello' in output, got %q", text)
				}
			},
		)
	})

	// Legal 2: cat on an inside absolute path is allowed — path-outside-allowed
	// does not fire because the path is under the allow-list.
	t.Run("cat_inside_absolute", func(t *testing.T) {
		inside := filepath.Join(fx.allowedDir, "hello.sh.txt")
		assertAccepted(t, reg, "bash",
			mustJSON(t, map[string]any{"command": fmt.Sprintf("cat %s", inside)}),
			"bash/cat_inside_absolute",
			func(text string) {
				if !strings.Contains(text, "hello via bash") {
					t.Errorf("expected file content in output, got %q", text)
				}
			},
		)
	})

	// Legal 3: cd to an absolute path inside the allow-list — no classification
	// bump per the design's "cd <literal> inside allowed" row.
	t.Run("cd_inside_allowed", func(t *testing.T) {
		cls := guardian.Classify("cd " + fx.allowedDir)
		if cls.Tier == bash.TierBlocked {
			t.Errorf("expected cd inside allowed dir to not be Blocked, got tier=%s rule=%s", cls.Tier, cls.Rule)
		}
		// Also execute end-to-end to confirm the BashTool does not reject.
		assertAccepted(t, reg, "bash",
			mustJSON(t, map[string]any{"command": fmt.Sprintf("cd %s && pwd", fx.allowedDir)}),
			"bash/cd_inside_allowed", nil)
	})

	// Legal 4: a relative-path argument — guardian leaves these as TierCaution
	// (resolution happens in the shell under cmd.Dir).
	t.Run("relative_arg", func(t *testing.T) {
		assertAccepted(t, reg, "bash",
			mustJSON(t, map[string]any{"command": "ls hello.sh.txt"}),
			"bash/relative_arg", nil)
	})
}
