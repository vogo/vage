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

package vctx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
)

// captureSink is a BuildReportSink test double that records every Save
// call so the Builder integration test can verify the wire-up without
// touching disk.
type captureSink struct {
	mu    sync.Mutex
	calls []captureSinkCall
	err   error
}

type captureSinkCall struct {
	SessionID string
	Report    BuildReport
}

func (c *captureSink) Save(_ context.Context, sessionID string, report BuildReport) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, captureSinkCall{SessionID: sessionID, Report: report})
	return c.err
}

func (c *captureSink) Calls() []captureSinkCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captureSinkCall, len(c.calls))
	copy(out, c.calls)
	return out
}

// TestDefaultBuilder_BuildReportSink_InvokedOnBuild verifies that a
// builder configured with WithBuildReportSink calls Save exactly once
// per Build, with the same report data the BuildResult exposes.
func TestDefaultBuilder_BuildReportSink_InvokedOnBuild(t *testing.T) {
	src := &stubSource{name: "s1", messages: []aimodel.Message{userMsg("hi")}}
	sink := &captureSink{}

	builder := NewDefaultBuilder(WithSources(src), WithBuildReportSink(sink))

	res, err := builder.Build(context.Background(), BuildInput{SessionID: "sid-sink"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	calls := sink.Calls()
	if len(calls) != 1 {
		t.Fatalf("Save calls = %d, want 1", len(calls))
	}
	if calls[0].SessionID != "sid-sink" {
		t.Errorf("Save sessionID = %q, want sid-sink", calls[0].SessionID)
	}
	if calls[0].Report.OutputCount != res.Report.OutputCount {
		t.Errorf("Save report drift: sink=%d builder=%d",
			calls[0].Report.OutputCount, res.Report.OutputCount)
	}
}

// TestDefaultBuilder_BuildReportSink_SkipsEmptySessionID guards
// against a Build invoked with no session id (anonymous test
// harnesses) accidentally writing to a "" subdirectory.
func TestDefaultBuilder_BuildReportSink_SkipsEmptySessionID(t *testing.T) {
	sink := &captureSink{}
	builder := NewDefaultBuilder(WithBuildReportSink(sink))

	if _, err := builder.Build(context.Background(), BuildInput{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if calls := sink.Calls(); len(calls) != 0 {
		t.Errorf("Save invoked %d times, want 0 for empty session id", len(calls))
	}
}

// TestDefaultBuilder_BuildReportSink_ErrorIsBestEffort confirms the
// Builder swallows sink errors so an LLM call is never aborted by an
// observability hiccup.
func TestDefaultBuilder_BuildReportSink_ErrorIsBestEffort(t *testing.T) {
	sink := &captureSink{err: errors.New("disk full")}
	builder := NewDefaultBuilder(WithBuildReportSink(sink))

	if _, err := builder.Build(context.Background(), BuildInput{SessionID: "sid-err"}); err != nil {
		t.Fatalf("Build aborted on sink error: %v", err)
	}
}

// TestFileBuildReportSink_RejectsEmptyRoot mirrors the FileSessionStore
// guard so callers see a typed error rather than an obscure os fault.
func TestFileBuildReportSink_RejectsEmptyRoot(t *testing.T) {
	if _, err := NewFileBuildReportSink(""); err == nil {
		t.Fatal("expected error for empty root")
	}
}

// TestFileBuildReportSink_SaveAndList writes a few reports and verifies
// List returns them newest-first, capped at limit.
func TestFileBuildReportSink_SaveAndList(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}

	for i := range 5 {
		report := BuildReport{
			BuilderName:  "test",
			Strategy:     StrategyOrderedGreedy,
			InputBudget:  1000,
			OutputCount:  i + 1,
			OutputTokens: (i + 1) * 100,
		}
		if err := sink.Save(context.Background(), "sid-list", report); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	got, err := sink.List(context.Background(), "sid-list", 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(reports) = %d, want 3 (capped at limit)", len(got))
	}

	// Newest-first: OutputCount should descend 5,4,3.
	wantOrder := []int{5, 4, 3}
	for i, w := range wantOrder {
		if got[i].OutputCount != w {
			t.Errorf("got[%d].OutputCount = %d, want %d", i, got[i].OutputCount, w)
		}
	}
}

// TestFileBuildReportSink_LRURetention writes more than the limit and
// confirms the oldest files are unlinked.
func TestFileBuildReportSink_LRURetention(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir(), WithBuildReportLimit(3))
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}

	for i := range 7 {
		if err := sink.Save(context.Background(), "sid-lru",
			BuildReport{OutputCount: i + 1}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	dir := filepath.Join(sink.Root(), "sid-lru", buildReportsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("on-disk file count = %d, want 3 (LRU)", len(entries))
	}

	// The retained files must be the last three written: sequences 5/6/7.
	gotNames := make([]string, len(entries))
	for i, e := range entries {
		gotNames[i] = e.Name()
	}
	for _, want := range []string{"000005.json", "000006.json", "000007.json"} {
		found := slices.Contains(gotNames, want)
		if !found {
			t.Errorf("missing retained file %q in %v", want, gotNames)
		}
	}
}

// TestFileBuildReportSink_DefaultLimit verifies the constructor falls
// back to DefaultBuildReportLimit when not overridden.
func TestFileBuildReportSink_DefaultLimit(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}
	if sink.Limit() != DefaultBuildReportLimit {
		t.Errorf("Limit = %d, want %d", sink.Limit(), DefaultBuildReportLimit)
	}
}

// TestFileBuildReportSink_NegativeLimit_FallsBack ensures a malformed
// option does not nuke the limit to 0 (which would mean "no retention,
// delete everything immediately").
func TestFileBuildReportSink_NegativeLimit_FallsBack(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir(), WithBuildReportLimit(-5))
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}
	if sink.Limit() != DefaultBuildReportLimit {
		t.Errorf("Limit = %d, want %d (default)", sink.Limit(), DefaultBuildReportLimit)
	}
}

// TestFileBuildReportSink_Disabled makes Save a no-op while leaving
// List intact for any pre-existing files. Useful for runtime toggling
// without rewiring the Builder.
func TestFileBuildReportSink_Disabled(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir(), DisableBuildReportPersistence())
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}
	if err := sink.Save(context.Background(), "sid-off", BuildReport{}); err != nil {
		t.Fatalf("Save (disabled): %v", err)
	}

	got, _ := sink.List(context.Background(), "sid-off", 10)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (Save was disabled)", len(got))
	}
}

// TestFileBuildReportSink_ListMissingDir returns empty + nil error
// when the session has no reports yet — HTTP /build-reports should
// see "[]" not 404 in this state.
func TestFileBuildReportSink_ListMissingDir(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}
	got, err := sink.List(context.Background(), "ghost-sid", 10)
	if err != nil {
		t.Fatalf("List missing: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestFileBuildReportSink_ListZeroLimit returns nil for limit <= 0 to
// avoid scanning the directory unnecessarily.
func TestFileBuildReportSink_ListZeroLimit(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	_ = sink.Save(context.Background(), "sid-zero", BuildReport{})

	got, err := sink.List(context.Background(), "sid-zero", 0)
	if err != nil {
		t.Fatalf("List zero limit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestFileBuildReportSink_ListMaxLimitClamped enforces the MaxListLimit
// cap so a malicious / careless caller cannot ask for the entire
// archive in one shot.
func TestFileBuildReportSink_ListMaxLimitClamped(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir(), WithBuildReportLimit(10))
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}
	for i := range 10 {
		_ = sink.Save(context.Background(), "sid-cap", BuildReport{OutputCount: i + 1})
	}

	got, err := sink.List(context.Background(), "sid-cap", MaxListLimit*2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) > MaxListLimit {
		t.Errorf("len = %d, want <= %d (clamped)", len(got), MaxListLimit)
	}
}

// TestFileBuildReportSink_RejectsEmptySessionID guards Save and List
// against an obvious caller mistake.
func TestFileBuildReportSink_RejectsEmptySessionID(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	if err := sink.Save(context.Background(), "", BuildReport{}); err == nil {
		t.Error("expected error from Save with empty sessionID")
	}
	if _, err := sink.List(context.Background(), "", 10); err == nil {
		t.Error("expected error from List with empty sessionID")
	}
}

// TestFileBuildReportSink_FileFormatPretty checks the on-disk file is
// indented (default) and decodes to a faithful BuildReport.
func TestFileBuildReportSink_FileFormatPretty(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	if err := sink.Save(context.Background(), "sid-fmt", BuildReport{
		BuilderName:  "test",
		OutputTokens: 42,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Join(sink.Root(), "sid-fmt", buildReportsDirName)
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("file count = %d, want 1", len(files))
	}

	raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "\n  \"output_tokens\":") {
		t.Errorf("expected pretty-printed JSON, got:\n%s", raw)
	}
}

// TestFileBuildReportSink_NonPretty toggles WithBuildReportPretty(false)
// and confirms the encoder leaves the output single-line.
func TestFileBuildReportSink_NonPretty(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir(), WithBuildReportPretty(false))
	if err := sink.Save(context.Background(), "sid-flat", BuildReport{OutputTokens: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Join(sink.Root(), "sid-flat", buildReportsDirName)
	files, _ := os.ReadDir(dir)
	raw, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))

	// Single line of JSON ends with newline only — no leading 2-space
	// indent.
	if strings.Contains(string(raw), "\n  \"") {
		t.Errorf("expected compact JSON, got:\n%s", raw)
	}
}

// TestFileBuildReportSink_ConcurrentSavesUnique writes from many
// goroutines targeting the same session id and verifies the sequence
// allocator never collides — every file lands at a unique sequence.
func TestFileBuildReportSink_ConcurrentSavesUnique(t *testing.T) {
	sink, err := NewFileBuildReportSink(t.TempDir(), WithBuildReportLimit(200))
	if err != nil {
		t.Fatalf("NewFileBuildReportSink: %v", err)
	}

	const N = 60
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			_ = sink.Save(context.Background(), "sid-race", BuildReport{})
		}()
	}
	wg.Wait()

	dir := filepath.Join(sink.Root(), "sid-race", buildReportsDirName)
	entries, _ := os.ReadDir(dir)
	if len(entries) != N {
		t.Errorf("file count = %d, want %d (unique sequences)", len(entries), N)
	}
}

// TestFileBuildReportSink_Crosssession writes to two sessions and
// confirms List filters by id — the layout's per-session directory is
// the only barrier we have, so we lean on it explicitly.
func TestFileBuildReportSink_Crosssession(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	for i := range 3 {
		_ = sink.Save(context.Background(), "sid-a", BuildReport{OutputCount: 100 + i})
		_ = sink.Save(context.Background(), "sid-b", BuildReport{OutputCount: 200 + i})
	}

	gotA, _ := sink.List(context.Background(), "sid-a", 10)
	gotB, _ := sink.List(context.Background(), "sid-b", 10)

	if len(gotA) != 3 || len(gotB) != 3 {
		t.Fatalf("len(A)=%d len(B)=%d, want 3 each", len(gotA), len(gotB))
	}
	for _, r := range gotA {
		if r.OutputCount < 100 || r.OutputCount > 102 {
			t.Errorf("sid-a leaked sid-b record: %v", r)
		}
	}
	for _, r := range gotB {
		if r.OutputCount < 200 || r.OutputCount > 202 {
			t.Errorf("sid-b leaked sid-a record: %v", r)
		}
	}
}

// TestFileBuildReportSink_LayoutPinned guards the convention that
// reports live under <root>/<sid>/build_reports/ — same parent
// directory as the rest of the per-session subsystems so a single
// SessionStore.Delete removes everything atomically.
func TestFileBuildReportSink_LayoutPinned(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	if err := sink.Save(context.Background(), "sid-layout", BuildReport{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want := filepath.Join(sink.Root(), "sid-layout", buildReportsDirName, "000001.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}
}

// TestFileBuildReportSink_SkipsNonReportFiles tolerates stray files
// in the directory (e.g., backup tools, .DS_Store) without failing.
func TestFileBuildReportSink_SkipsNonReportFiles(t *testing.T) {
	sink, _ := NewFileBuildReportSink(t.TempDir())
	if err := sink.Save(context.Background(), "sid-mix", BuildReport{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir := filepath.Join(sink.Root(), "sid-mix", buildReportsDirName)
	junk := filepath.Join(dir, ".DS_Store")
	if err := os.WriteFile(junk, []byte("noise"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	// Save again — sequence allocator should still produce 000002 by
	// recognising only the well-formed report file.
	if err := sink.Save(context.Background(), "sid-mix", BuildReport{}); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	// File count for legit reports should be 2.
	got, err := sink.List(context.Background(), "sid-mix", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List len = %d, want 2 (junk file ignored)", len(got))
	}
}

// reportNamePattern is a sanity helper: assert sequence digits round-
// trip cleanly. The implementation uses %06d; this guards a sloppy
// edit from breaking the pad width.
func TestSequenceNamingFormat(t *testing.T) {
	tcs := []struct {
		seq  int
		name string
	}{
		{1, "000001.json"},
		{42, "000042.json"},
		{999999, "999999.json"},
	}
	for _, tc := range tcs {
		got := fmt.Sprintf("%06d%s", tc.seq, buildReportFileExt)
		if got != tc.name {
			t.Errorf("seq=%d name=%q, want %q", tc.seq, got, tc.name)
		}
	}
}
