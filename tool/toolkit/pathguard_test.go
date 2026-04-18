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

package toolkit

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPathGuard_Empty(t *testing.T) {
	g, err := NewPathGuard(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if g.Allowed() {
		t.Error("empty guard should report Allowed()==false")
	}

	if _, _, _, err := g.Check("test", "/etc/passwd"); err == nil {
		t.Error("expected Check on empty guard to error")
	}
}

func TestPathGuard_RejectsRelative(t *testing.T) {
	if _, err := NewPathGuard([]string{"relative/dir"}); err == nil {
		t.Error("expected error for non-existent relative dir")
	}
}

func TestPathGuard_RejectsFilesystemRoot(t *testing.T) {
	if _, err := NewPathGuard([]string{"/"}); err == nil {
		t.Error("expected error for filesystem root")
	}
}

func TestPathGuard_RejectsNonExistentDir(t *testing.T) {
	if _, err := NewPathGuard([]string{"/nonexistent/path/xyz"}); err == nil {
		t.Error("expected error for non-existent dir")
	}
}

func TestPathGuard_Dedupe(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	g, err := NewPathGuard([]string{parent, child})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = g.Close() }()

	if len(g.Dirs()) != 1 {
		t.Errorf("expected 1 dir after containment dedupe, got %d: %v", len(g.Dirs()), g.Dirs())
	}
}

func TestPathGuard_Check_Accepts(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	target := filepath.Join(dir, "file.txt")

	cleaned, rel, root, err := g.Check("test", target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cleaned == "" || rel == "" || root == nil {
		t.Errorf("incomplete return: cleaned=%q rel=%q root=%v", cleaned, rel, root)
	}
}

func TestPathGuard_Check_RejectsOutside(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	if _, _, _, err := g.Check("test", "/etc/passwd"); err == nil {
		t.Error("expected rejection for path outside allowed dir")
	}
}

func TestPathGuard_Check_RejectsRelative(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	if _, _, _, err := g.Check("test", "relative.txt"); err == nil {
		t.Error("expected rejection for relative path")
	}
}

func TestPathGuard_Check_RejectsUNC(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	cases := []string{
		`\\server\share\file`,
		`\\?\C:\Windows\file`,
		`\\.\COM1`,
	}
	for _, p := range cases {
		if _, _, _, err := g.Check("test", p); err == nil {
			t.Errorf("expected rejection for UNC-style path %q", p)
		}
	}
}

func TestPathGuard_OpenForRead_SymlinkOutside(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()

	// Write the target outside.
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink INSIDE allowed pointing outside.
	link := filepath.Join(allowed, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	g, err := NewPathGuard([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	// Reading the symlink path must fail (os.Root refuses escape).
	f, _, err := g.OpenForRead("test", link)
	if err == nil {
		_ = f.Close()
		t.Error("expected OpenForRead to fail for symlink pointing outside allowed")
	}
}

func TestPathGuard_OpenForWrite_Create(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	target := filepath.Join(dir, "out.txt")

	f, _, err := g.OpenForWrite("test", target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = f.Close()

	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected file to be written: %v", err)
	}
}

func TestPathGuard_Concurrent(t *testing.T) {
	dir := t.TempDir()

	g, err := NewPathGuard([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			_, _, _, _ = g.Check("test", filepath.Join(dir, "x.txt"))
		})
	}

	wg.Wait()
}

func TestPathGuard_FormatAllowedDirs(t *testing.T) {
	short := FormatAllowedDirs([]string{"/a", "/b"})
	if !strings.Contains(short, "/a") || !strings.Contains(short, "/b") {
		t.Errorf("short list formatting failed: %s", short)
	}

	long := FormatAllowedDirs([]string{"/a", "/b", "/c", "/d", "/e"})
	if !strings.Contains(long, "+2 more") {
		t.Errorf("long list should indicate elided entries: %s", long)
	}
}

func TestCanonicalizeDirs_Dedupe(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := CanonicalizeDirs([]string{child, parent})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Errorf("expected 1 after dedupe, got %d: %v", len(got), got)
	}
}
