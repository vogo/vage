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
	"sort"
	"sync"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600

	// On-disk layout under <root>/<sessionID>/workspace/.
	workspaceDirName = "workspace"
	planFileName     = "plan.md"
	notesDirName     = "notes"
	noteFileSuffix   = ".md"
)

// FileWorkspace persists per-session plan + notes under a root directory.
// On-disk layout:
//
//	<root>/<sessionID>/workspace/plan.md
//	<root>/<sessionID>/workspace/notes/<name>.md
//
// The root is shared with vage/session.FileSessionStore by convention so
// that a single os.RemoveAll(<root>/<sessionID>) wipes both the session
// records and the workspace; the SessionStore.Delete method already does
// this, so callers do not need to coordinate.
//
// Concurrency: writes against the same session are serialised by a per-
// session mutex (allocated lazily). Reads are intentionally lock-free —
// atomic-renamed file writes mean a concurrent reader either sees the
// previous version or the new one. Cross-process coordination is not
// provided; running multiple writers against the same root is undefined.
type FileWorkspace struct {
	root  string
	locks sync.Map // map[string]*sync.Mutex
}

// Compile-time conformance check.
var _ Workspace = (*FileWorkspace)(nil)

// NewFileWorkspace constructs a workspace rooted at the given directory.
// The directory is created (with parents) if it does not exist; an empty
// root returns ErrInvalidArgument.
func NewFileWorkspace(root string) (*FileWorkspace, error) {
	if root == "" {
		return nil, errors.New("workspace: root directory is empty")
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("workspace: create root %q: %w", root, err)
	}
	return &FileWorkspace{root: root}, nil
}

// Root returns the configured root directory. Useful for tests and logging.
func (w *FileWorkspace) Root() string { return w.root }

// PathOf returns the on-disk workspace root for a session, even if the
// directory has not been materialised yet. Returns "" for invalid ids so
// the caller can rely on a non-empty result implying a usable path.
func (w *FileWorkspace) PathOf(sessionID string) string {
	if validateSessionID(sessionID) != nil {
		return ""
	}
	return filepath.Join(w.root, sessionID, workspaceDirName)
}

func (w *FileWorkspace) lockFor(id string) *sync.Mutex {
	v, _ := w.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (w *FileWorkspace) sessionDir(id string) string {
	return filepath.Join(w.root, id, workspaceDirName)
}

func (w *FileWorkspace) notesDir(id string) string {
	return filepath.Join(w.sessionDir(id), notesDirName)
}

func (w *FileWorkspace) planPath(id string) string {
	return filepath.Join(w.sessionDir(id), planFileName)
}

func (w *FileWorkspace) notePath(id, name string) string {
	return filepath.Join(w.notesDir(id), name+noteFileSuffix)
}

// --- Plan ---

// ReadPlan reads plan.md and returns its contents. A missing plan is
// reported as ("", nil) so the LLM tool layer can return an empty answer
// without ferrying os.IsNotExist around.
func (w *FileWorkspace) ReadPlan(ctx context.Context, sessionID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}

	data, err := os.ReadFile(w.planPath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("workspace: read plan: %w", err)
	}
	return string(data), nil
}

// WritePlan replaces plan.md with content (atomic). Empty content removes
// plan.md so a subsequent ReadPlan returns ("", nil).
func (w *FileWorkspace) WritePlan(ctx context.Context, sessionID, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if len(content) > MaxPlanBytes {
		return fmt.Errorf("%w: plan size %d exceeds %d", ErrTooLarge, len(content), MaxPlanBytes)
	}

	mu := w.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	if content == "" {
		if err := os.Remove(w.planPath(sessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace: remove plan: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(w.sessionDir(sessionID), dirPerm); err != nil {
		return fmt.Errorf("workspace: create dir: %w", err)
	}
	return writeFileAtomic(w.planPath(sessionID), []byte(content))
}

// --- Notes ---

// ReadNote reads notes/<name>.md and returns its contents. Missing notes
// are reported as ("", nil); ErrInvalidName is returned for malformed names
// so the caller never reads through to the filesystem.
func (w *FileWorkspace) ReadNote(ctx context.Context, sessionID, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	if err := validateNoteName(name); err != nil {
		return "", err
	}

	data, err := os.ReadFile(w.notePath(sessionID, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("workspace: read note: %w", err)
	}
	return string(data), nil
}

// WriteNote writes notes/<name>.md atomically. Empty content removes the
// note. Adding a new note that would push the session past MaxNoteCount
// returns ErrTooManyNotes with no write — overwriting an existing note
// never trips the cap.
func (w *FileWorkspace) WriteNote(ctx context.Context, sessionID, name, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateNoteName(name); err != nil {
		return err
	}
	if len(content) > MaxNoteBytes {
		return fmt.Errorf("%w: note %q size %d exceeds %d", ErrTooLarge, name, len(content), MaxNoteBytes)
	}

	mu := w.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	notePath := w.notePath(sessionID, name)
	if content == "" {
		if err := os.Remove(notePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace: remove note: %w", err)
		}
		return nil
	}

	// Cap check applies only to *new* notes; updating an existing one is
	// always allowed. Performed under the lock so we cannot race past the cap.
	if _, err := os.Stat(notePath); errors.Is(err, os.ErrNotExist) {
		count, listErr := w.countNotesLocked(sessionID)
		if listErr != nil {
			return listErr
		}
		if count >= MaxNoteCount {
			return fmt.Errorf("%w: %d notes already present", ErrTooManyNotes, count)
		}
	} else if err != nil {
		return fmt.Errorf("workspace: stat note: %w", err)
	}

	if err := os.MkdirAll(w.notesDir(sessionID), dirPerm); err != nil {
		return fmt.Errorf("workspace: create notes dir: %w", err)
	}
	return writeFileAtomic(notePath, []byte(content))
}

// ListNotes returns the index of notes for a session. The list is sorted
// by UpdatedAt DESC, then by name ASC for determinism.
func (w *FileWorkspace) ListNotes(ctx context.Context, sessionID string) ([]NoteInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}

	notesDir := w.notesDir(sessionID)
	entries, err := os.ReadDir(notesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []NoteInfo{}, nil
		}
		return nil, fmt.Errorf("workspace: read notes dir: %w", err)
	}

	out := make([]NoteInfo, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		entryName := ent.Name()
		base, suffix := splitNoteFile(entryName)
		if suffix != noteFileSuffix || base == "" {
			continue
		}
		// Filter out names that would have been rejected by validateNoteName,
		// guarding against manually-placed files in notes/ that the API
		// surface wouldn't normally produce.
		if validateNoteName(base) != nil {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		out = append(out, NoteInfo{
			Name:      base,
			Bytes:     int(info.Size()),
			UpdatedAt: info.ModTime(),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].Name < out[j].Name
	})

	return out, nil
}

// Delete removes the entire workspace tree for a session.
func (w *FileWorkspace) Delete(_ context.Context, sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		// Invalid id never had a workspace — idempotent.
		if errors.Is(err, ErrInvalidSession) {
			return nil
		}
		return err
	}

	mu := w.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	if err := os.RemoveAll(w.sessionDir(sessionID)); err != nil {
		return fmt.Errorf("workspace: delete: %w", err)
	}
	w.locks.Delete(sessionID)
	return nil
}

// countNotesLocked counts current note files. Caller must hold the per-
// session lock. Missing notes/ counts as zero; validation of file names is
// the same one used by ListNotes so the two stay consistent.
func (w *FileWorkspace) countNotesLocked(sessionID string) (int, error) {
	entries, err := os.ReadDir(w.notesDir(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("workspace: count notes: %w", err)
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		base, suffix := splitNoteFile(ent.Name())
		if suffix != noteFileSuffix || base == "" {
			continue
		}
		if validateNoteName(base) != nil {
			continue
		}
		n++
	}
	return n, nil
}

// splitNoteFile splits a filename into (base, suffix) where suffix is the
// last "." onward. It does NOT use filepath.Ext directly because we want
// the dot included in the suffix so callers can match noteFileSuffix
// (".md") verbatim.
func splitNoteFile(name string) (base, suffix string) {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i], name[i:]
		}
	}
	return name, ""
}

// writeFileAtomic encodes data to path via temp file + rename so concurrent
// readers either see the previous or the new file — never a partial write.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("workspace: open tmp: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("workspace: write: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("workspace: fsync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("workspace: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("workspace: rename: %w", err)
	}
	return nil
}
