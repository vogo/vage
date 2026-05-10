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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newScratchWS(t *testing.T) (*FileWorkspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewFileWorkspace(dir)
	if err != nil {
		t.Fatalf("NewFileWorkspace: %v", err)
	}
	return ws, dir
}

func TestScratch_RoundTrip(t *testing.T) {
	ws, root := newScratchWS(t)
	ctx := context.Background()
	const sid, slot, name = "sess-1", "child-a", "draft"

	if err := ws.WriteScratch(ctx, sid, slot, name, "hello"); err != nil {
		t.Fatalf("WriteScratch: %v", err)
	}

	wantPath := filepath.Join(root, sid, "workspace", "scratch", slot, name+".md")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected file at %q: %v", wantPath, err)
	}

	got, err := ws.ReadScratch(ctx, sid, slot, name)
	if err != nil {
		t.Fatalf("ReadScratch: %v", err)
	}
	if got != "hello" {
		t.Errorf("read = %q, want %q", got, "hello")
	}
}

func TestScratch_Overwrite(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid, slot, name = "s", "slot", "n"

	if err := ws.WriteScratch(ctx, sid, slot, name, "first"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := ws.WriteScratch(ctx, sid, slot, name, "second"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, err := ws.ReadScratch(ctx, sid, slot, name)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "second" {
		t.Errorf("read = %q, want second", got)
	}
}

func TestScratch_EmptyContentDeletes(t *testing.T) {
	// Symmetric with notes: an empty write removes the entry so a
	// subsequent ReadScratch returns ("", nil).
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid, slot, name = "s", "slot", "n"

	if err := ws.WriteScratch(ctx, sid, slot, name, "data"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ws.WriteScratch(ctx, sid, slot, name, ""); err != nil {
		t.Fatalf("delete via empty write: %v", err)
	}
	got, err := ws.ReadScratch(ctx, sid, slot, name)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "" {
		t.Errorf("read after delete = %q, want empty", got)
	}
}

func TestScratch_ReadMissingReturnsEmpty(t *testing.T) {
	ws, _ := newScratchWS(t)
	got, err := ws.ReadScratch(context.Background(), "s", "slot", "missing")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("got = %q, want empty", got)
	}
}

func TestScratch_ListOrdersByUpdatedAtDesc(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid, slot = "s", "slot"

	for _, n := range []string{"a", "b", "c"} {
		if err := ws.WriteScratch(ctx, sid, slot, n, n); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}

	got, err := ws.ListScratch(ctx, sid, slot)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// b and c written after a; the most recent should be first.
	if got[0].Name != "c" {
		t.Errorf("first = %q, want c", got[0].Name)
	}
}

func TestScratch_ListMissingSlotIsEmpty(t *testing.T) {
	ws, _ := newScratchWS(t)
	got, err := ws.ListScratch(context.Background(), "s", "fresh-slot")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestScratch_DeleteSlotRemovesAllEntries(t *testing.T) {
	ws, root := newScratchWS(t)
	ctx := context.Background()
	const sid, slot = "s", "doomed"

	for _, n := range []string{"a", "b"} {
		if err := ws.WriteScratch(ctx, sid, slot, n, n); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	if err := ws.DeleteScratchSlot(ctx, sid, slot); err != nil {
		t.Fatalf("delete slot: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, sid, "workspace", "scratch", slot)); !os.IsNotExist(err) {
		t.Errorf("slot dir still present: %v", err)
	}
	got, err := ws.ListScratch(ctx, sid, slot)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestScratch_DeleteSlotIdempotent(t *testing.T) {
	ws, _ := newScratchWS(t)
	if err := ws.DeleteScratchSlot(context.Background(), "s", "missing-slot"); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestScratch_SlotsAreIsolated(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid = "s"

	if err := ws.WriteScratch(ctx, sid, "alpha", "n", "in-alpha"); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := ws.WriteScratch(ctx, sid, "beta", "n", "in-beta"); err != nil {
		t.Fatalf("write beta: %v", err)
	}

	gotA, _ := ws.ReadScratch(ctx, sid, "alpha", "n")
	gotB, _ := ws.ReadScratch(ctx, sid, "beta", "n")
	if gotA != "in-alpha" || gotB != "in-beta" {
		t.Errorf("alpha=%q beta=%q, want in-alpha/in-beta", gotA, gotB)
	}

	if err := ws.DeleteScratchSlot(ctx, sid, "alpha"); err != nil {
		t.Fatalf("delete alpha: %v", err)
	}
	gotA, _ = ws.ReadScratch(ctx, sid, "alpha", "n")
	gotB, _ = ws.ReadScratch(ctx, sid, "beta", "n")
	if gotA != "" {
		t.Errorf("alpha still present: %q", gotA)
	}
	if gotB != "in-beta" {
		t.Errorf("beta wiped by alpha delete: %q", gotB)
	}
}

func TestScratch_RejectsInvalidSlot(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()

	cases := []string{"", ".", "..", "with/slash", strings.Repeat("a", SlotNameMaxLen+1)}
	for _, slot := range cases {
		t.Run(fmt.Sprintf("slot=%q", slot), func(t *testing.T) {
			if err := ws.WriteScratch(ctx, "s", slot, "n", "x"); !errors.Is(err, ErrInvalidSlot) {
				t.Errorf("write err = %v, want ErrInvalidSlot", err)
			}
			if _, err := ws.ReadScratch(ctx, "s", slot, "n"); !errors.Is(err, ErrInvalidSlot) {
				t.Errorf("read err = %v, want ErrInvalidSlot", err)
			}
			if _, err := ws.ListScratch(ctx, "s", slot); !errors.Is(err, ErrInvalidSlot) {
				t.Errorf("list err = %v, want ErrInvalidSlot", err)
			}
			if err := ws.DeleteScratchSlot(ctx, "s", slot); !errors.Is(err, ErrInvalidSlot) {
				t.Errorf("delete err = %v, want ErrInvalidSlot", err)
			}
		})
	}
}

func TestScratch_RejectsInvalidName(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()

	cases := []string{"", ".", "..", "with/slash", strings.Repeat("a", NoteNameMaxLen+1)}
	for _, name := range cases {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			if err := ws.WriteScratch(ctx, "s", "slot", name, "x"); !errors.Is(err, ErrInvalidName) {
				t.Errorf("write err = %v, want ErrInvalidName", err)
			}
			if _, err := ws.ReadScratch(ctx, "s", "slot", name); !errors.Is(err, ErrInvalidName) {
				t.Errorf("read err = %v, want ErrInvalidName", err)
			}
		})
	}
}

func TestScratch_RejectsInvalidSession(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	if err := ws.WriteScratch(ctx, "", "slot", "n", "x"); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
}

func TestScratch_RejectsTooLarge(t *testing.T) {
	ws, root := newScratchWS(t)
	ctx := context.Background()
	body := strings.Repeat("a", MaxScratchBytesPerFile+1)
	if err := ws.WriteScratch(ctx, "s", "slot", "n", body); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(root, "s", "workspace", "scratch", "slot", "n.md")); !os.IsNotExist(err) {
		t.Errorf("expected no file, stat err = %v", err)
	}
}

func TestScratch_FileCountCap(t *testing.T) {
	// Filling the slot to MaxScratchFilesPerSlot should succeed; one
	// more new entry trips ErrTooManyScratch. Updating an existing
	// entry never trips the cap.
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid, slot = "s", "slot"

	for i := range MaxScratchFilesPerSlot {
		if err := ws.WriteScratch(ctx, sid, slot, fmt.Sprintf("e-%03d", i), "x"); err != nil {
			t.Fatalf("WriteScratch %d: %v", i, err)
		}
	}
	if err := ws.WriteScratch(ctx, sid, slot, "overflow", "x"); !errors.Is(err, ErrTooManyScratch) {
		t.Fatalf("err = %v, want ErrTooManyScratch", err)
	}
	// Updating an existing entry remains fine.
	if err := ws.WriteScratch(ctx, sid, slot, "e-000", "y"); err != nil {
		t.Errorf("update existing should pass: %v", err)
	}
}

func TestScratch_DeletedWithSession(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid, slot = "s", "slot"

	if err := ws.WriteScratch(ctx, sid, slot, "n", "data"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ws.Delete(ctx, sid); err != nil {
		t.Fatalf("session delete: %v", err)
	}
	got, err := ws.ReadScratch(ctx, sid, slot, "n")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "" {
		t.Errorf("post-delete = %q, want empty", got)
	}
}

func TestScratch_NotIndexedByListNotes(t *testing.T) {
	// scratch entries live in workspace/scratch/<slot>/<name>.md and
	// must not surface through ListNotes (which walks workspace/notes).
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid = "s"

	if err := ws.WriteNote(ctx, sid, "real-note", "n"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if err := ws.WriteScratch(ctx, sid, "slot", "draft", "x"); err != nil {
		t.Fatalf("WriteScratch: %v", err)
	}

	notes, err := ws.ListNotes(ctx, sid)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Name != "real-note" {
		t.Errorf("ListNotes = %+v, want exactly real-note", notes)
	}
}

func TestScratch_DoesNotConsumeNoteCount(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx := context.Background()
	const sid = "s"

	for i := range MaxNoteCount {
		if err := ws.WriteNote(ctx, sid, fmt.Sprintf("n-%03d", i), "x"); err != nil {
			t.Fatalf("WriteNote %d: %v", i, err)
		}
	}
	if err := ws.WriteNote(ctx, sid, "overflow", "x"); !errors.Is(err, ErrTooManyNotes) {
		t.Fatalf("expected note overflow, got %v", err)
	}
	// Scratch is unaffected.
	for i := range 5 {
		if err := ws.WriteScratch(ctx, sid, "slot", fmt.Sprintf("e-%d", i), "x"); err != nil {
			t.Errorf("WriteScratch %d: %v", i, err)
		}
	}
}

func TestScratch_Concurrent(t *testing.T) {
	// 10 sessions × 5 slots × 4 entries written concurrently.
	ws, _ := newScratchWS(t)
	ctx := context.Background()

	const nSessions, nSlots, nEntries = 10, 5, 4
	var wg sync.WaitGroup
	wg.Add(nSessions * nSlots * nEntries)
	for s := range nSessions {
		for sl := range nSlots {
			for e := range nEntries {
				sid := fmt.Sprintf("sess-%02d", s)
				slot := fmt.Sprintf("slot-%d", sl)
				name := fmt.Sprintf("e-%d", e)
				body := fmt.Sprintf("%s/%s/%s", sid, slot, name)
				go func() {
					defer wg.Done()
					if err := ws.WriteScratch(ctx, sid, slot, name, body); err != nil {
						t.Errorf("write %s/%s/%s: %v", sid, slot, name, err)
					}
				}()
			}
		}
	}
	wg.Wait()

	for s := range nSessions {
		for sl := range nSlots {
			sid := fmt.Sprintf("sess-%02d", s)
			slot := fmt.Sprintf("slot-%d", sl)
			got, err := ws.ListScratch(ctx, sid, slot)
			if err != nil {
				t.Errorf("list %s/%s: %v", sid, slot, err)
				continue
			}
			if len(got) != nEntries {
				t.Errorf("%s/%s len=%d want=%d", sid, slot, len(got), nEntries)
			}
		}
	}
}

func TestScratch_CtxCancel(t *testing.T) {
	ws, _ := newScratchWS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ws.WriteScratch(ctx, "s", "slot", "n", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("write err = %v, want context.Canceled", err)
	}
	if _, err := ws.ReadScratch(ctx, "s", "slot", "n"); !errors.Is(err, context.Canceled) {
		t.Errorf("read err = %v, want context.Canceled", err)
	}
	if _, err := ws.ListScratch(ctx, "s", "slot"); !errors.Is(err, context.Canceled) {
		t.Errorf("list err = %v, want context.Canceled", err)
	}
	if err := ws.DeleteScratchSlot(ctx, "s", "slot"); !errors.Is(err, context.Canceled) {
		t.Errorf("delete err = %v, want context.Canceled", err)
	}
}
