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
	"testing"
)

func TestAtomicWriteInRoot_Create(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := AtomicWriteInRoot(root, "new.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}

	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestAtomicWriteInRoot_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()

	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := AtomicWriteInRoot(root, "existing.txt", []byte("new content"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "new content" {
		t.Errorf("content = %q, want %q", string(data), "new content")
	}
}

func TestAtomicWriteInRoot_NestedDir(t *testing.T) {
	dir := t.TempDir()

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := AtomicWriteInRoot(root, "sub/file.txt", []byte("nested"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sub, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "nested" {
		t.Errorf("content = %q, want %q", string(data), "nested")
	}

	// No temp files in sub/.
	entries, err := os.ReadDir(sub)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind in sub/: %s", e.Name())
		}
	}
}

func TestAtomicWriteInRoot_PreservesPermission(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := AtomicWriteInRoot(root, "perm.txt", []byte("data"), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "perm.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0o600", got)
	}
}
