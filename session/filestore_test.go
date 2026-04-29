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

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func newTestFileStore(t *testing.T) *FileSessionStore {
	t.Helper()
	st, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	return st
}

func TestFileStore_DirectoryLayout(t *testing.T) {
	st := newTestFileStore(t)
	if err := st.Create(context.Background(), New("layout")); err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := filepath.Join(st.Root(), "layout")
	for _, name := range []string{metaFilename, eventsFilename, stateFilename} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s, got %v", name, err)
		}
	}
}

func TestFileStore_AppendEventDoesNotRewriteMeta(t *testing.T) {
	st := newTestFileStore(t)
	if err := st.Create(context.Background(), New("meta")); err != nil {
		t.Fatalf("create: %v", err)
	}
	metaPath := filepath.Join(st.Root(), "meta", metaFilename)
	beforeStat, _ := os.Stat(metaPath)
	beforeMod := beforeStat.ModTime()

	for range 5 {
		ev := schema.NewEvent(schema.EventAgentStart, "", "meta", schema.AgentStartData{})
		if err := st.AppendEvent(context.Background(), "meta", ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	afterStat, _ := os.Stat(metaPath)
	if !afterStat.ModTime().Equal(beforeMod) {
		t.Fatalf("AppendEvent should not touch meta.json: before %v, after %v", beforeMod, afterStat.ModTime())
	}
}

func TestFileStore_EventLineFormatMatchesTracelog(t *testing.T) {
	st := newTestFileStore(t)
	if err := st.Create(context.Background(), New("fmt")); err != nil {
		t.Fatalf("create: %v", err)
	}
	ev := schema.NewEvent(schema.EventAgentStart, "agent", "fmt", schema.AgentStartData{})
	if err := st.AppendEvent(context.Background(), "fmt", ev); err != nil {
		t.Fatalf("append: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(st.Root(), "fmt", eventsFilename))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("missing trailing newline: %q", data)
	}
	// Each line must round-trip via json.Marshal(schema.Event).
	expected, _ := json.Marshal(ev)
	expected = append(expected, '\n')
	if string(data) != string(expected) {
		t.Fatalf("line mismatch.\n got: %q\nwant: %q", data, expected)
	}
}

func TestFileStore_AtomicWriteLeavesNoTmp(t *testing.T) {
	st := newTestFileStore(t)
	if err := st.Create(context.Background(), New("atom")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.SetState(context.Background(), "atom", "k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	dir := filepath.Join(st.Root(), "atom")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("orphan tmp file: %s", e.Name())
		}
	}
}

func TestFileStore_ListSkipsCorruptDir(t *testing.T) {
	st := newTestFileStore(t)
	if err := st.Create(context.Background(), New("ok")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Create a directory with a valid id-shape but no meta.json — must be skipped.
	if err := os.Mkdir(filepath.Join(st.Root(), "broken"), filestoreDirPerm); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := st.List(context.Background(), SessionFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("expected only 'ok', got %+v", got)
	}
}

func TestFileStore_ReopenExisting(t *testing.T) {
	root := t.TempDir()
	st1, _ := NewFileSessionStore(root)
	if err := st1.Create(context.Background(), New("persist")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st1.SetState(context.Background(), "persist", "k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Reopen a fresh store at the same root and read everything back.
	st2, err := NewFileSessionStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := st2.Get(context.Background(), "persist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "persist" {
		t.Fatalf("unexpected: %+v", got)
	}
	v, ok, _ := st2.GetState(context.Background(), "persist", "k")
	if !ok || v != "v" {
		t.Fatalf("state lost: ok=%v v=%v", ok, v)
	}
}

func TestFileStore_RejectsEmptyRoot(t *testing.T) {
	if _, err := NewFileSessionStore(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}
