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

package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newArtifactWS(t *testing.T) (*FileWorkspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewFileWorkspace(dir)
	if err != nil {
		t.Fatalf("NewFileWorkspace: %v", err)
	}
	return ws, dir
}

func TestWriteArtifact_RoundTrip(t *testing.T) {
	ws, root := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess-1"
	const name = "elided-abc.txt"
	body := []byte("hello world")

	gotPath, err := ws.WriteArtifact(ctx, sid, name, body)
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	wantPath := filepath.Join(root, sid, "workspace", "artifacts", name)
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}

	got, err := ws.ReadArtifact(ctx, sid, name)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read = %q, want %q", got, body)
	}
}

func TestWriteArtifact_Overwrite(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess-1"
	const name = "log.txt"

	if _, err := ws.WriteArtifact(ctx, sid, name, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := ws.WriteArtifact(ctx, sid, name, []byte("second")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, err := ws.ReadArtifact(ctx, sid, name)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("read = %q, want \"second\"", got)
	}
}

func TestWriteArtifact_EmptyContentWritesEmptyFile(t *testing.T) {
	// Artifacts are write-once references; unlike notes, empty content
	// must NOT delete the artifact (callers rely on the path existing).
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess-1"
	const name = "empty.txt"

	if _, err := ws.WriteArtifact(ctx, sid, name, nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	got, err := ws.ReadArtifact(ctx, sid, name)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("read len = %d, want 0", len(got))
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), sid, "workspace", "artifacts", name)); err != nil {
		t.Errorf("expected file to exist after empty write: %v", err)
	}
}

func TestReadArtifact_MissingReturnsNilNil(t *testing.T) {
	ws, _ := newArtifactWS(t)
	got, err := ws.ReadArtifact(context.Background(), "sess-1", "nope.txt")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("data = %v, want nil", got)
	}
}

func TestArtifact_RejectsInvalidName(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess-1"

	cases := []string{"", ".", "..", "with/slash.txt", strings.Repeat("a", NoteNameMaxLen+1)}
	for _, name := range cases {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			if _, err := ws.WriteArtifact(ctx, sid, name, []byte("x")); !errors.Is(err, ErrInvalidName) {
				t.Errorf("WriteArtifact err = %v, want ErrInvalidName", err)
			}
			if _, err := ws.ReadArtifact(ctx, sid, name); !errors.Is(err, ErrInvalidName) {
				t.Errorf("ReadArtifact err = %v, want ErrInvalidName", err)
			}
		})
	}
}

func TestArtifact_RejectsInvalidSession(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	if _, err := ws.WriteArtifact(ctx, "", "n.txt", []byte("x")); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
	if _, err := ws.ReadArtifact(ctx, "", "n.txt"); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
}

func TestArtifact_RejectsTooLarge(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	body := make([]byte, MaxArtifactBytes+1)
	if _, err := ws.WriteArtifact(ctx, "sess", "big.txt", body); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	// Make sure no partial file was left behind.
	if _, err := os.Stat(filepath.Join(ws.Root(), "sess", "workspace", "artifacts", "big.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no artifact file, got stat err = %v", err)
	}
}

func TestArtifact_NotIndexedByListNotes(t *testing.T) {
	// Artifacts must live in a sibling dir to notes/ so ListNotes
	// (which scans notes/) doesn't surface them. Verify by writing one
	// of each then listing.
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess"

	if err := ws.WriteNote(ctx, sid, "real-note", "n"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if _, err := ws.WriteArtifact(ctx, sid, "art.txt", []byte("a")); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	notes, err := ws.ListNotes(ctx, sid)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Name != "real-note" {
		t.Errorf("ListNotes = %+v, want exactly real-note", notes)
	}
}

func TestArtifact_DoesNotConsumeNoteCount(t *testing.T) {
	// Write MaxNoteCount notes, then write 5 artifacts — neither write
	// should fail with ErrTooManyNotes.
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess"

	for i := range MaxNoteCount {
		if err := ws.WriteNote(ctx, sid, fmt.Sprintf("n-%03d", i), "x"); err != nil {
			t.Fatalf("WriteNote %d: %v", i, err)
		}
	}
	// New note would now overflow.
	if err := ws.WriteNote(ctx, sid, "overflow", "x"); !errors.Is(err, ErrTooManyNotes) {
		t.Fatalf("expected overflow on note %d, got %v", MaxNoteCount, err)
	}
	// Artifacts are unaffected.
	for i := range 5 {
		name := fmt.Sprintf("a-%d.txt", i)
		if _, err := ws.WriteArtifact(ctx, sid, name, []byte("x")); err != nil {
			t.Errorf("WriteArtifact %s: %v", name, err)
		}
	}
}

func TestArtifact_DeletedWithSession(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx := context.Background()
	const sid = "sess"

	if _, err := ws.WriteArtifact(ctx, sid, "a.txt", []byte("data")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ws.Delete(ctx, sid); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := ws.ReadArtifact(ctx, sid, "a.txt")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if got != nil {
		t.Errorf("post-delete read = %q, want nil", got)
	}
}

func TestArtifact_Concurrent(t *testing.T) {
	// 20 sessions × 5 artifacts each, written concurrently. Verify
	// each round-trips.
	ws, _ := newArtifactWS(t)
	ctx := context.Background()

	const nSessions, nArtifacts = 20, 5
	var wg sync.WaitGroup
	wg.Add(nSessions * nArtifacts)
	for s := range nSessions {
		for a := range nArtifacts {
			sid := fmt.Sprintf("sess-%02d", s)
			name := fmt.Sprintf("a-%d.txt", a)
			body := fmt.Appendf(nil, "%s/%s", sid, name)
			go func() {
				defer wg.Done()
				if _, err := ws.WriteArtifact(ctx, sid, name, body); err != nil {
					t.Errorf("write %s/%s: %v", sid, name, err)
				}
			}()
		}
	}
	wg.Wait()

	for s := range nSessions {
		for a := range nArtifacts {
			sid := fmt.Sprintf("sess-%02d", s)
			name := fmt.Sprintf("a-%d.txt", a)
			want := fmt.Appendf(nil, "%s/%s", sid, name)
			got, err := ws.ReadArtifact(ctx, sid, name)
			if err != nil {
				t.Errorf("read %s/%s: %v", sid, name, err)
				continue
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/%s mismatch: got %q, want %q", sid, name, got, want)
			}
		}
	}
}

func TestArtifact_CtxCancel(t *testing.T) {
	ws, _ := newArtifactWS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ws.WriteArtifact(ctx, "sess", "a.txt", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("write err = %v, want context.Canceled", err)
	}
	if _, err := ws.ReadArtifact(ctx, "sess", "a.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("read err = %v, want context.Canceled", err)
	}
}
