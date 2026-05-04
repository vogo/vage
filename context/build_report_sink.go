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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

// BuildReportSink persists a BuildReport per Build call. The Builder
// invokes Save(ctx, sessionID, report) inline after assembling the
// report; sinks must be fast (small JSON write or noop) because the
// call sits on the LLM hot path.
//
// Implementations must be safe for concurrent use. Errors are logged
// by the Builder and dropped — BuildReport persistence is observability,
// not correctness.
type BuildReportSink interface {
	Save(ctx context.Context, sessionID string, report BuildReport) error
}

// BuildReportReader is the read side of the build-report archive.
// Provided as a distinct interface so HTTP / CLI consumers can depend
// on a narrow contract without dragging the Save signature into their
// dep graph (and vice-versa for the Builder).
type BuildReportReader interface {
	// List returns the most recent reports for sessionID in newest-first
	// order, capped at limit (limit <= 0 returns nothing). The returned
	// slice is freshly allocated and owned by the caller.
	List(ctx context.Context, sessionID string, limit int) ([]BuildReport, error)
}

// DefaultBuildReportLimit caps the per-session retention of reports for
// FileBuildReportSink. Long Runs can produce hundreds of reports so the
// LRU bound prevents disk explosion; vv plumbs a configurable override.
const DefaultBuildReportLimit = 50

// MaxListLimit caps how many reports a single List call can pull. Even
// when retention allows more, callers should paginate rather than dump
// the whole archive in one response.
const MaxListLimit = 500

const (
	buildReportsDirName    = "build_reports"
	buildReportFileExt     = ".json"
	buildReportSequenceLen = 6 // matches checkpoint filestore — easier to eyeball
	buildReportDirPerm     = os.FileMode(0o700)
	buildReportFilePerm    = os.FileMode(0o600)
)

// buildReportFilePattern recognises the on-disk layout
// <NNNNNN>.json that FileBuildReportSink writes. Captured group 1
// holds the zero-padded sequence digits.
var buildReportFilePattern = regexp.MustCompile(`^(\d{6})\.json$`)

// FileBuildReportSink writes one BuildReport per call as
// <root>/<sessionID>/build_reports/<NNNNNN>.json. The directory shape
// matches FileSessionStore's <root>/<id>/ convention so a
// SessionStore.Delete(<root>/<id>) wipes the reports alongside meta /
// events / checkpoints / metrics.
//
// The store enforces an LRU retention limit per session: when Save
// would push the count past the limit, the oldest files are unlinked
// in sequence order. The retention bound is per-session (not global)
// so a busy session does not crowd out a quiet one.
type FileBuildReportSink struct {
	root     string
	limit    int
	pretty   bool
	locks    sync.Map // map[sessionID]*sync.Mutex; ensures sequence allocation is atomic
	disabled bool
}

// Compile-time interface conformance.
var (
	_ BuildReportSink   = (*FileBuildReportSink)(nil)
	_ BuildReportReader = (*FileBuildReportSink)(nil)
)

// FileBuildReportSinkOption configures a FileBuildReportSink.
type FileBuildReportSinkOption func(*FileBuildReportSink)

// WithBuildReportLimit overrides the per-session retention cap. Values
// <= 0 fall back to DefaultBuildReportLimit. To disable persistence
// without removing the wiring, pass DisableBuildReportPersistence.
func WithBuildReportLimit(n int) FileBuildReportSinkOption {
	return func(s *FileBuildReportSink) {
		if n <= 0 {
			n = DefaultBuildReportLimit
		}
		s.limit = n
	}
}

// WithBuildReportPretty toggles indented JSON output. Default true so
// `cat <NNNNNN>.json` is human-readable; set false in high-write
// environments to save bytes.
func WithBuildReportPretty(on bool) FileBuildReportSinkOption {
	return func(s *FileBuildReportSink) { s.pretty = on }
}

// DisableBuildReportPersistence makes Save a no-op. Useful for
// runtime-toggling persistence via the same wired sink without having
// to rebuild the Builder. List still scans whatever exists on disk so
// previously-written reports remain readable.
func DisableBuildReportPersistence() FileBuildReportSinkOption {
	return func(s *FileBuildReportSink) { s.disabled = true }
}

// NewFileBuildReportSink constructs a sink rooted at the given dir.
// The root is created (with parents) if absent. Returns an error
// when root is empty.
func NewFileBuildReportSink(root string, opts ...FileBuildReportSinkOption) (*FileBuildReportSink, error) {
	if root == "" {
		return nil, errors.New("vctx: build_report root is empty")
	}
	if err := os.MkdirAll(root, buildReportDirPerm); err != nil {
		return nil, fmt.Errorf("vctx: create build_report root %q: %w", root, err)
	}
	s := &FileBuildReportSink{
		root:   root,
		limit:  DefaultBuildReportLimit,
		pretty: true,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Root returns the configured root directory; useful in tests.
func (s *FileBuildReportSink) Root() string { return s.root }

// Limit returns the active retention cap.
func (s *FileBuildReportSink) Limit() int { return s.limit }

func (s *FileBuildReportSink) lockFor(sessionID string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(sessionID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *FileBuildReportSink) reportsDir(sessionID string) string {
	return filepath.Join(s.root, sessionID, buildReportsDirName)
}

// Save persists report for sessionID. Allocates the next sequence
// number under a per-session mutex, writes atomically, then trims old
// files past the LRU limit. The function is best-effort by contract:
// IO errors return up to the caller, but the Builder treats them as
// non-fatal observability misses.
func (s *FileBuildReportSink) Save(ctx context.Context, sessionID string, report BuildReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sessionID == "" {
		return errors.New("vctx: session id is empty")
	}
	if s.disabled {
		return nil
	}

	mu := s.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	dir := s.reportsDir(sessionID)
	if err := os.MkdirAll(dir, buildReportDirPerm); err != nil {
		return fmt.Errorf("vctx: mkdir build_reports %q: %w", dir, err)
	}

	seq, existing, err := s.allocateSequenceLocked(dir)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, fmt.Sprintf("%06d%s", seq, buildReportFileExt))
	if err := writeReportAtomic(path, report, s.pretty); err != nil {
		return err
	}

	if err := s.trimLocked(dir, append(existing, seq)); err != nil {
		// Trim failure does not invalidate the just-written report; log
		// upstream by returning the wrapped error so the caller can
		// decide whether to surface it.
		return fmt.Errorf("vctx: trim build_reports: %w", err)
	}
	return nil
}

// List returns the most recent reports up to limit, newest-first. A
// missing directory yields an empty slice (not an error) so HTTP
// callers see "no reports yet" rather than a 404 when the user asks
// before the first Run.
func (s *FileBuildReportSink) List(ctx context.Context, sessionID string, limit int) ([]BuildReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, errors.New("vctx: session id is empty")
	}
	if limit <= 0 {
		return nil, nil
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	dir := s.reportsDir(sessionID)
	seqs, err := scanReportSequences(dir)
	if err != nil {
		return nil, err
	}
	if len(seqs) == 0 {
		return nil, nil
	}

	// scanReportSequences returns ascending; iterate from the end so
	// the slice is filled newest-first.
	out := make([]BuildReport, 0, min(limit, len(seqs)))
	for i := len(seqs) - 1; i >= 0 && len(out) < limit; i-- {
		path := filepath.Join(dir, fmt.Sprintf("%06d%s", seqs[i], buildReportFileExt))
		report, err := readReportFile(path)
		if err != nil {
			// One bad file should not poison the whole list — log via
			// returned error path rather than panicking. We surface the
			// first decode error so callers know something is wrong.
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}

// allocateSequenceLocked scans dir for existing report files and
// returns the next sequence number plus the existing ascending
// sequence list (used by trimLocked to LRU-evict). Caller must hold
// the per-session mutex.
func (s *FileBuildReportSink) allocateSequenceLocked(dir string) (int, []int, error) {
	existing, err := scanReportSequences(dir)
	if err != nil {
		return 0, nil, err
	}
	next := 1
	if len(existing) > 0 {
		next = existing[len(existing)-1] + 1
	}
	return next, existing, nil
}

// trimLocked enforces the LRU retention bound by unlinking the
// oldest files when the count exceeds s.limit. allSeqs must be the
// ascending sequence list including the just-written entry. Caller
// must hold the per-session mutex.
func (s *FileBuildReportSink) trimLocked(dir string, allSeqs []int) error {
	if len(allSeqs) <= s.limit {
		return nil
	}
	// Re-sort defensively in case the caller appended out of order.
	sort.Ints(allSeqs)
	excess := len(allSeqs) - s.limit
	for i := range excess {
		path := filepath.Join(dir, fmt.Sprintf("%06d%s", allSeqs[i], buildReportFileExt))
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// scanReportSequences enumerates all <NNNNNN>.json files in dir and
// returns their sequence numbers in ascending order. A missing
// directory yields nil + nil so the caller can treat "no reports
// yet" as an empty list.
func scanReportSequences(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("vctx: read build_reports %q: %w", dir, err)
	}
	seqs := make([]int, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := buildReportFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		seqs = append(seqs, n)
	}
	sort.Ints(seqs)
	return seqs, nil
}

// writeReportAtomic encodes report to path via tmp+rename so a
// concurrent reader either sees the previous fully-written file or
// the new one — never a torn write.
func writeReportAtomic(path string, report BuildReport, pretty bool) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, buildReportFilePerm)
	if err != nil {
		return fmt.Errorf("vctx: open tmp %q: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	enc := json.NewEncoder(f)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err = enc.Encode(report); err != nil {
		_ = f.Close()
		return fmt.Errorf("vctx: encode report: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("vctx: fsync report: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("vctx: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vctx: rename %q -> %q: %w", tmp, path, err)
	}
	return nil
}

// readReportFile decodes a single build_report JSON file.
func readReportFile(path string) (BuildReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return BuildReport{}, fmt.Errorf("vctx: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out BuildReport
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return BuildReport{}, fmt.Errorf("vctx: decode %q: %w", path, err)
	}
	return out, nil
}
