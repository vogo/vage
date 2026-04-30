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

package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileIterationStore_Conformance(t *testing.T) {
	runStoreContract(t, "file", func(t *testing.T) IterationStore {
		root := t.TempDir()
		s, err := NewFileIterationStore(root)
		if err != nil {
			t.Fatalf("NewFileIterationStore: %v", err)
		}
		return s
	})
}

func TestNewFileIterationStore_RejectsEmptyRoot(t *testing.T) {
	_, err := NewFileIterationStore("")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

// TestFileIterationStore_FileNameLayout verifies the on-disk layout so
// future tooling (e.g. ops scripts) can rely on it.
func TestFileIterationStore_FileNameLayout(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileIterationStore(root)
	if err != nil {
		t.Fatalf("NewFileIterationStore: %v", err)
	}
	cp := newTestCheckpoint("sess-1", 0, false, "")
	if err := s.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Join(root, "sess-1", "checkpoints")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("ReadDir len = %d, want 1", len(ents))
	}
	name := ents[0].Name()
	wantPrefix := "000001-"
	if !strings.HasPrefix(name, wantPrefix) {
		t.Errorf("name = %q, want prefix %q", name, wantPrefix)
	}
	if !strings.HasSuffix(name, ".json") {
		t.Errorf("name = %q, want .json suffix", name)
	}
}

// TestFileIterationStore_TmpFileNotCounted verifies a stale .json.tmp
// file does not bump nextSequence (atomic-write residue from a previous
// crash should be ignored).
func TestFileIterationStore_TmpFileNotCounted(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileIterationStore(root)
	if err != nil {
		t.Fatalf("NewFileIterationStore: %v", err)
	}

	dir := filepath.Join(root, "sess-tmp", "checkpoints")
	if err := os.MkdirAll(dir, filestoreDirPerm); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Leave a stale tmp file.
	if err := os.WriteFile(
		filepath.Join(dir, "000099-deadbeef.json.tmp"),
		[]byte("{}"), filestoreFilePerm,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cp := newTestCheckpoint("sess-tmp", 0, false, "")
	if err := s.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if cp.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1 (tmp file should be ignored)", cp.Sequence)
	}
}
