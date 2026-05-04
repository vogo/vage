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
	"sync"
	"testing"
)

func newTestFileMetricsStore(t *testing.T) *FileMetricsStore {
	t.Helper()
	store, err := NewFileMetricsStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileMetricsStore: %v", err)
	}
	return store
}

// TestFileMetricsStore_NewFileMetricsStore_RejectsEmpty mirrors the
// session FileSessionStore contract: callers should hit ErrInvalidArgument
// rather than an obscure os error when they pass "".
func TestFileMetricsStore_NewFileMetricsStore_RejectsEmpty(t *testing.T) {
	if _, err := NewFileMetricsStore(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

// TestFileMetricsStore_RoundTrip writes a record then reads it back
// across freshly-constructed stores so we exercise the on-disk path
// without any in-memory bleed.
func TestFileMetricsStore_RoundTrip(t *testing.T) {
	root := t.TempDir()

	w, err := NewFileMetricsStore(root)
	if err != nil {
		t.Fatalf("NewFileMetricsStore: %v", err)
	}

	if err := w.Update(context.Background(), "sid-rt", func(m *SessionMetrics) {
		m.PromptTokens = 42
		m.CompletionTokens = 8
		m.ResumeCount = 3
		m.CostUSD = 0.0125
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Brand-new store handle on the same root — proves the roundtrip
	// is genuine disk IO, not in-process map.
	r, err := NewFileMetricsStore(root)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}

	got, err := r.Get(context.Background(), "sid-rt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PromptTokens != 42 || got.CompletionTokens != 8 {
		t.Errorf("counters drift: %+v", got)
	}
	if got.TotalTokens != 50 {
		t.Errorf("TotalTokens = %d, want 50", got.TotalTokens)
	}
	if got.ResumeCount != 3 {
		t.Errorf("ResumeCount = %d", got.ResumeCount)
	}
}

// TestFileMetricsStore_OnDiskLayoutPinned guards the convention that
// every per-session subsystem writes under <root>/<id>/ so a single
// SessionStore.Delete (os.RemoveAll) wipes everything.
func TestFileMetricsStore_OnDiskLayoutPinned(t *testing.T) {
	store := newTestFileMetricsStore(t)
	if err := store.Update(context.Background(), "layout-test", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	want := filepath.Join(store.Root(), "layout-test", "metrics.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}
}

// TestFileMetricsStore_DeleteRemovesFile checks Delete idempotence and
// that it actually unlinks the file (so a follow-up Get returns
// ErrMetricsNotFound).
func TestFileMetricsStore_DeleteRemovesFile(t *testing.T) {
	store := newTestFileMetricsStore(t)
	if err := store.Update(context.Background(), "sid-del", func(m *SessionMetrics) {
		m.PromptTokens = 1
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := store.Delete(context.Background(), "sid-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get(context.Background(), "sid-del"); !errors.Is(err, ErrMetricsNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrMetricsNotFound", err)
	}

	// Idempotent — a second Delete on a missing file is fine.
	if err := store.Delete(context.Background(), "sid-del"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

// TestFileMetricsStore_GetMissing returns ErrMetricsNotFound when no
// Update has run for the id (file does not exist).
func TestFileMetricsStore_GetMissing(t *testing.T) {
	store := newTestFileMetricsStore(t)
	if _, err := store.Get(context.Background(), "no-such-sid"); !errors.Is(err, ErrMetricsNotFound) {
		t.Fatalf("err = %v, want ErrMetricsNotFound", err)
	}
}

// TestFileMetricsStore_FileFormatIsHumanReadable checks that the JSON
// on disk is the wire format — pretty-printed, no surprises, and
// every public field is present so ops can `cat metrics.json`.
func TestFileMetricsStore_FileFormatIsHumanReadable(t *testing.T) {
	store := newTestFileMetricsStore(t)
	if err := store.Update(context.Background(), "sid-fmt", func(m *SessionMetrics) {
		m.PromptTokens = 7
		m.CompletionTokens = 3
		m.CostUSD = 0.001
		m.ResumeCount = 1
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	path := filepath.Join(store.Root(), "sid-fmt", metricsFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Pretty-printed by writeJSONAtomic via SetIndent.
	if want := []byte("\n  \"prompt_tokens\":"); !contains(raw, want) {
		t.Errorf("missing pretty-printed prompt_tokens line in:\n%s", raw)
	}

	// Decode with the canonical struct so we know the schema is intact.
	var got SessionMetrics
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode persisted file: %v", err)
	}
	if got.SessionID != "sid-fmt" || got.PromptTokens != 7 || got.TotalTokens != 10 {
		t.Errorf("persisted struct mismatch: %+v", got)
	}
}

// TestFileMetricsStore_ConcurrentUpdates exercises the per-session
// mutex: distinct sessions must not contend, the same session must
// serialise. We mix 50 distinct sessions × 4 concurrent updates each.
func TestFileMetricsStore_ConcurrentUpdates(t *testing.T) {
	store := newTestFileMetricsStore(t)

	var wg sync.WaitGroup
	const sessions = 50
	const updatesPer = 4
	for i := range sessions {
		sid := sidFor(i)
		for range updatesPer {
			wg.Go(func() {
				_ = store.Update(context.Background(), sid, func(m *SessionMetrics) {
					m.PromptTokens++
				})
			})
		}
	}
	wg.Wait()

	for i := range sessions {
		sid := sidFor(i)
		got, err := store.Get(context.Background(), sid)
		if err != nil {
			t.Fatalf("Get %q: %v", sid, err)
		}
		if got.PromptTokens != updatesPer {
			t.Errorf("session %q: PromptTokens = %d, want %d", sid, got.PromptTokens, updatesPer)
		}
	}
}

// sidFor builds a deterministic session id for the concurrency test —
// validateID requires [A-Za-z0-9._-]{1,128}.
func sidFor(i int) string {
	return "concurrent-" + intToA(i)
}

// intToA stringifies a small int without depending on strconv to keep
// the test file's import surface tight.
func intToA(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}

// contains is a byte-substring helper kept local to the test file so
// the metrics tests do not pull in a string-search package just to
// check pretty-printed output.
func contains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := range needle {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
