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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	filestoreDirPerm   os.FileMode = 0o700
	filestoreFilePerm  os.FileMode = 0o600
	checkpointsDirName             = "checkpoints"
	checkpointFileExt              = ".json"
	checkpointTmpExt               = ".json.tmp"
	sequenceWidth                  = 6 // zero-padded prefix width in file names
)

// FileIterationStore persists checkpoints under a root directory using
// the layout:
//
//	<root>/<session_id>/checkpoints/<NNNNNN>-<id>.json
//
// where <NNNNNN> is a 6-digit zero-padded Sequence and <id> is the
// 8-byte hex Checkpoint.ID. ls sorts the directory in Sequence order
// and the prefix lets sequence allocation use a single ReadDir scan.
//
// The directory layout intentionally mirrors vage/session.FileSessionStore
// so ops can co-locate the two; the package itself does NOT reference
// session, keeping checkpoint independent.
//
// Concurrency: writes against the same session are serialized via a
// per-session sync.Mutex (lazily allocated via sync.Map). Cross-process
// coordination is NOT provided.
type FileIterationStore struct {
	root  string
	locks sync.Map // map[string]*sync.Mutex
}

// Compile-time check.
var _ IterationStore = (*FileIterationStore)(nil)

// NewFileIterationStore creates the store rooted at the given directory,
// creating it (with parents) if it does not exist.
func NewFileIterationStore(root string) (*FileIterationStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root directory is empty", ErrInvalidArgument)
	}
	if err := os.MkdirAll(root, filestoreDirPerm); err != nil {
		return nil, fmt.Errorf("checkpoint: create root %q: %w", root, err)
	}
	return &FileIterationStore{root: root}, nil
}

// Root returns the configured root directory; useful in tests.
func (s *FileIterationStore) Root() string { return s.root }

func (s *FileIterationStore) lockFor(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *FileIterationStore) checkpointsDir(sessionID string) string {
	return filepath.Join(s.root, sessionID, checkpointsDirName)
}

// Save assigns Sequence/ID/CreatedAt, then writes the JSON payload
// atomically.
func (s *FileIterationStore) Save(ctx context.Context, cp *Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp == nil {
		return fmt.Errorf("%w: checkpoint is nil", ErrInvalidArgument)
	}
	if cp.SessionID == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	mu := s.lockFor(cp.SessionID)
	mu.Lock()
	defer mu.Unlock()

	dir := s.checkpointsDir(cp.SessionID)
	if err := os.MkdirAll(dir, filestoreDirPerm); err != nil {
		return fmt.Errorf("checkpoint: mkdir %q: %w", dir, err)
	}

	next, err := nextSequence(dir)
	if err != nil {
		return err
	}

	cp.Sequence = next
	cp.ID = generateID()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}

	name := fmt.Sprintf("%0*d-%s%s", sequenceWidth, cp.Sequence, cp.ID, checkpointFileExt)
	if err := writeJSONAtomic(filepath.Join(dir, name), cp); err != nil {
		return err
	}
	return nil
}

// Load reads the file matching id (or the highest sequence when id is
// empty) and decodes it.
func (s *FileIterationStore) Load(ctx context.Context, sessionID, id string) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	dir := s.checkpointsDir(sessionID)
	entries, err := readSequenceFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, ErrCheckpointNotFound
	}

	var match *fileEntry
	if id == "" {
		// Highest sequence == latest. entries already in ascending order.
		last := entries[len(entries)-1]
		match = &last
	} else {
		for i := range entries {
			if entries[i].id == id {
				match = &entries[i]
				break
			}
		}
	}
	if match == nil {
		return nil, ErrCheckpointNotFound
	}

	return readCheckpoint(filepath.Join(dir, match.name))
}

// List enumerates checkpoint files and decodes their metadata-only
// view (still requires reading each file because metadata is stored
// inside the JSON payload; for v1 this is fine — file counts stay in
// the hundreds at most per session).
func (s *FileIterationStore) List(ctx context.Context, sessionID string) ([]*CheckpointMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	dir := s.checkpointsDir(sessionID)
	entries, err := readSequenceFiles(dir)
	if err != nil {
		return nil, err
	}

	out := make([]*CheckpointMeta, 0, len(entries))
	for _, ent := range entries {
		cp, err := readCheckpoint(filepath.Join(dir, ent.name))
		if err != nil {
			// Skip half-written / corrupt files rather than fail the list,
			// matching FileSessionStore.List policy.
			continue
		}
		out = append(out, metaFrom(cp))
	}
	return out, nil
}

// Delete recursively removes the checkpoints directory for sessionID.
func (s *FileIterationStore) Delete(_ context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	mu := s.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	dir := s.checkpointsDir(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("checkpoint: delete %q: %w", dir, err)
	}
	s.locks.Delete(sessionID)
	return nil
}

// --- internal helpers ---

// fileEntry describes one checkpoint file: its on-disk name, the parsed
// sequence prefix, and the embedded id.
type fileEntry struct {
	name     string
	sequence int
	id       string
}

// readSequenceFiles returns every "<NNNNNN>-<id>.json" entry in dir
// in ascending Sequence order. Non-conforming names (including the
// .json.tmp atomic-write temp file) are skipped silently. A missing
// directory is returned as an empty slice with no error so List on an
// untouched session does not surface as an error.
func readSequenceFiles(dir string) ([]fileEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint: read dir %q: %w", dir, err)
	}

	out := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, checkpointFileExt) ||
			strings.HasSuffix(name, checkpointTmpExt) {
			continue
		}
		base := strings.TrimSuffix(name, checkpointFileExt)
		dash := strings.IndexByte(base, '-')
		if dash <= 0 || dash == len(base)-1 {
			continue
		}
		seq, err := strconv.Atoi(base[:dash])
		if err != nil {
			continue
		}
		out = append(out, fileEntry{name: name, sequence: seq, id: base[dash+1:]})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].sequence < out[j].sequence })
	return out, nil
}

// nextSequence returns one past the highest existing Sequence in dir,
// or 1 if dir is empty / missing.
func nextSequence(dir string) (int, error) {
	entries, err := readSequenceFiles(dir)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 1, nil
	}
	return entries[len(entries)-1].sequence + 1, nil
}

func readCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrCheckpointNotFound
		}
		return nil, fmt.Errorf("checkpoint: read %q: %w", path, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint: decode %q: %w", path, err)
	}
	return &cp, nil
}

// writeJSONAtomic encodes v to path via temp file + rename.
func writeJSONAtomic(path string, v any) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filestoreFilePerm)
	if err != nil {
		return fmt.Errorf("checkpoint: open tmp: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(v); err != nil {
		_ = f.Close()
		return fmt.Errorf("checkpoint: encode: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("checkpoint: fsync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("checkpoint: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}
