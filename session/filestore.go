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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/vogo/vage/schema"
)

const (
	filestoreDirPerm  os.FileMode = 0o700
	filestoreFilePerm os.FileMode = 0o600

	metaFilename   = "meta.json"
	eventsFilename = "events.jsonl"
	stateFilename  = "state.json"
)

// FileSessionStore persists Session metadata, events, and state under a root
// directory. The on-disk layout is:
//
//	<root>/<session_id>/meta.json     // metadata, atomic-rewritten on Update
//	<root>/<session_id>/events.jsonl  // one JSON-marshalled schema.Event per
//	                                  // line, append-only — byte-for-byte
//	                                  // compatible with vv/traces/tracelog
//	<root>/<session_id>/state.json    // structured state KV, atomic-rewritten
//
// AppendEvent intentionally does NOT touch meta.json: a typical ReAct loop
// emits dozens to hundreds of events per task, and an atomic rewrite per
// event is wasted I/O. UpdatedAt therefore tracks "last metadata or state
// change" — see SessionMetaStore.Update — and is eventually consistent with
// the event log. Callers that need precise "last activity" can stat
// events.jsonl themselves.
//
// Concurrency: writes against the same session id are serialised via a
// per-session mutex (allocated lazily). Reads are intentionally lock-free
// and so may race with a concurrent Delete; a racing reader can observe a
// session that vanishes mid-call and surface that as ErrSessionNotFound.
// This is the same eventual-consistency contract that, e.g., OpenAI
// Threads exposes. Cross-process coordination is NOT provided; running
// multiple writer processes against the same root is undefined.
type FileSessionStore struct {
	root  string
	locks sync.Map // map[string]*sync.Mutex
}

// Compile-time check.
var _ SessionStore = (*FileSessionStore)(nil)

// NewFileSessionStore constructs a store rooted at the given directory. The
// directory is created (with parents) if it does not exist.
func NewFileSessionStore(root string) (*FileSessionStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root directory is empty", ErrInvalidArgument)
	}
	if err := os.MkdirAll(root, filestoreDirPerm); err != nil {
		return nil, fmt.Errorf("session: create root %q: %w", root, err)
	}
	return &FileSessionStore{root: root}, nil
}

// Root returns the configured root directory. Useful for tests.
func (s *FileSessionStore) Root() string { return s.root }

func (s *FileSessionStore) lockFor(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *FileSessionStore) sessionDir(id string) string {
	return filepath.Join(s.root, id)
}

// --- SessionMetaStore ---

// Create writes the session directory and seed files.
func (s *FileSessionStore) Create(ctx context.Context, in *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidArgument)
	}
	if err := validateID(in.ID); err != nil {
		return err
	}

	mu := s.lockFor(in.ID)
	mu.Lock()
	defer mu.Unlock()

	dir := s.sessionDir(in.ID)
	if _, err := os.Stat(dir); err == nil {
		return ErrSessionExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: stat %q: %w", dir, err)
	}

	if err := os.Mkdir(dir, filestoreDirPerm); err != nil {
		return fmt.Errorf("session: mkdir %q: %w", dir, err)
	}

	cloned := cloneSession(in)
	if cloned.State == "" {
		cloned.State = StateActive
	}
	now := time.Now()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = now
	}
	cloned.UpdatedAt = now

	if err := writeJSONAtomic(filepath.Join(dir, metaFilename), cloned); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("session: write meta: %w", err)
	}
	// Touch events file so subsequent appends know the session is real even
	// if the user hasn't appended anything yet. Empty file is valid JSONL.
	ef, err := os.OpenFile(filepath.Join(dir, eventsFilename), os.O_CREATE|os.O_WRONLY, filestoreFilePerm)
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("session: create events file: %w", err)
	}
	_ = ef.Close()

	if err := writeJSONAtomic(filepath.Join(dir, stateFilename), map[string]any{}); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("session: write state: %w", err)
	}
	return nil
}

// Get reads meta.json for id.
func (s *FileSessionStore) Get(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	path := filepath.Join(s.sessionDir(id), metaFilename)
	out, err := readMeta(path)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update overwrites meta.json. UpdatedAt is set to time.Now().
func (s *FileSessionStore) Update(ctx context.Context, in *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidArgument)
	}
	if err := validateID(in.ID); err != nil {
		return err
	}

	mu := s.lockFor(in.ID)
	mu.Lock()
	defer mu.Unlock()

	dir := s.sessionDir(in.ID)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrSessionNotFound
	} else if err != nil {
		return fmt.Errorf("session: stat %q: %w", dir, err)
	}

	existing, err := readMeta(filepath.Join(dir, metaFilename))
	if err != nil {
		return err
	}

	cloned := cloneSession(in)
	cloned.UpdatedAt = time.Now()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = existing.CreatedAt
	}

	return writeJSONAtomic(filepath.Join(dir, metaFilename), cloned)
}

// Delete removes the session directory recursively.
func (s *FileSessionStore) Delete(_ context.Context, id string) error {
	if err := validateID(id); err != nil {
		// Invalid id never existed — idempotent delete.
		if errors.Is(err, ErrInvalidArgument) {
			return nil
		}
		return err
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	dir := s.sessionDir(id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("session: delete %q: %w", dir, err)
	}
	s.locks.Delete(id)
	return nil
}

// List enumerates session directories under root, applying f.
func (s *FileSessionStore) List(ctx context.Context, f SessionFilter) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("session: read root %q: %w", s.root, err)
	}

	type idMeta struct {
		id   string
		meta *Session
	}
	collected := make([]idMeta, 0, len(entries))

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		id := ent.Name()
		if !IDPattern.MatchString(id) {
			continue
		}
		meta, err := readMeta(filepath.Join(s.root, id, metaFilename))
		if err != nil {
			// Skip half-written / corrupt sessions rather than fail the list.
			continue
		}
		collected = append(collected, idMeta{id: id, meta: meta})
	}

	// Stable order: by CreatedAt then by id, so List is reproducible.
	sort.SliceStable(collected, func(i, j int) bool {
		ci, cj := collected[i].meta.CreatedAt, collected[j].meta.CreatedAt
		if ci.Equal(cj) {
			return collected[i].id < collected[j].id
		}
		return ci.Before(cj)
	})

	out := make([]*Session, 0, len(collected))
	skipped := 0
	for _, im := range collected {
		if !sessionMatches(im.meta, f) {
			continue
		}
		if f.Offset > 0 && skipped < f.Offset {
			skipped++
			continue
		}
		out = append(out, im.meta)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// --- SessionEventStore ---

// AppendEvent appends one JSON-marshalled event (followed by '\n') to
// events.jsonl. Does NOT update meta.json by design — see type doc.
func (s *FileSessionStore) AppendEvent(ctx context.Context, id string, e schema.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	dir := s.sessionDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrSessionNotFound
	} else if err != nil {
		return fmt.Errorf("session: stat %q: %w", dir, err)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("session: marshal event: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(
		filepath.Join(dir, eventsFilename),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		filestoreFilePerm,
	)
	if err != nil {
		return fmt.Errorf("session: open events: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("session: write event: %w", err)
	}
	return nil
}

// ListEvents returns every event in append order by streaming events.jsonl
// line-by-line.
func (s *FileSessionStore) ListEvents(ctx context.Context, id string) ([]schema.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	dir := s.sessionDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("session: stat %q: %w", dir, err)
	}

	f, err := os.Open(filepath.Join(dir, eventsFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []schema.Event{}, nil
		}
		return nil, fmt.Errorf("session: open events: %w", err)
	}
	defer func() { _ = f.Close() }()

	out := make([]schema.Event, 0, 32)
	sc := bufio.NewScanner(f)
	// schema.Event payloads can be large (full tool output);
	// allow up to 8 MiB per line.
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw rawLineEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("session: decode event: %w", err)
		}
		out = append(out, raw.toEvent())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("session: scan events: %w", err)
	}
	return out, nil
}

// --- SessionStateStore ---

// GetState reads state.json and returns the value for key.
func (s *FileSessionStore) GetState(ctx context.Context, id, key string) (any, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validateID(id); err != nil {
		return nil, false, err
	}

	st, err := s.readState(id)
	if err != nil {
		return nil, false, err
	}
	v, present := st[key]
	return v, present, nil
}

// SetState rewrites state.json with key set to value.
func (s *FileSessionStore) SetState(ctx context.Context, id, key string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	st, err := s.readStateLocked(id)
	if err != nil {
		return err
	}
	st[key] = value
	if err := writeJSONAtomic(filepath.Join(s.sessionDir(id), stateFilename), st); err != nil {
		return fmt.Errorf("session: write state: %w", err)
	}
	return s.touchMetaLocked(id)
}

// DeleteState rewrites state.json without key. Idempotent on missing key.
func (s *FileSessionStore) DeleteState(ctx context.Context, id, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	st, err := s.readStateLocked(id)
	if err != nil {
		return err
	}
	if _, present := st[key]; !present {
		return nil
	}
	delete(st, key)
	if err := writeJSONAtomic(filepath.Join(s.sessionDir(id), stateFilename), st); err != nil {
		return fmt.Errorf("session: write state: %w", err)
	}
	return s.touchMetaLocked(id)
}

// ListState returns a copy of the state map.
func (s *FileSessionStore) ListState(ctx context.Context, id string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	st, err := s.readState(id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(st))
	maps.Copy(out, st)
	return out, nil
}

// --- internal helpers ---

// readState reads state.json without holding the per-session lock. Safe
// because state.json is replaced atomically.
func (s *FileSessionStore) readState(id string) (map[string]any, error) {
	dir := s.sessionDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("session: stat %q: %w", dir, err)
	}
	return readStateFile(filepath.Join(dir, stateFilename))
}

// readStateLocked is used after acquiring the per-session lock and so does
// not double-check existence races; it just reads the file.
func (s *FileSessionStore) readStateLocked(id string) (map[string]any, error) {
	dir := s.sessionDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrSessionNotFound
	}
	return readStateFile(filepath.Join(dir, stateFilename))
}

// touchMetaLocked rewrites meta.json with UpdatedAt = now. Caller must hold
// the per-session lock.
func (s *FileSessionStore) touchMetaLocked(id string) error {
	path := filepath.Join(s.sessionDir(id), metaFilename)
	meta, err := readMeta(path)
	if err != nil {
		return err
	}
	meta.UpdatedAt = time.Now()
	if err := writeJSONAtomic(path, meta); err != nil {
		return fmt.Errorf("session: write meta: %w", err)
	}
	return nil
}

func readMeta(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("session: read meta: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("session: decode meta: %w", err)
	}
	return &s, nil
}

func readStateFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Treat missing state.json as empty rather than an error: it's
			// a valid in-flight state for a session whose Create raced with
			// state writes.
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("session: read state: %w", err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	out := make(map[string]any)
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("session: decode state: %w", err)
	}
	return out, nil
}

// rawLineEvent is the on-disk wire format for events.jsonl. We cannot
// unmarshal directly into schema.Event because Event.Data is the named
// interface schema.EventData whose marker method is unexported; the json
// package has no way to decide which concrete type to instantiate. By
// storing Data as raw bytes we round-trip every top-level field cleanly and
// preserve the Data payload as json.RawMessage for callers that want to
// re-decode it themselves.
type rawLineEvent struct {
	Type      string          `json:"type"`
	AgentID   string          `json:"agent_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
	ParentID  string          `json:"parent_id,omitempty"`
}

// toEvent converts a rawLineEvent into a schema.Event with Data left nil.
// Callers needing the original Data payload can read events.jsonl
// themselves; the byte format matches vv/traces/tracelog exactly.
func (r *rawLineEvent) toEvent() schema.Event {
	return schema.Event{
		Type:      r.Type,
		AgentID:   r.AgentID,
		SessionID: r.SessionID,
		Timestamp: r.Timestamp,
		Data:      nil,
		ParentID:  r.ParentID,
	}
}

// writeJSONAtomic encodes v to path via a temp file + rename so that
// concurrent readers either see the previous fully-written file or the new
// one — never a partial write.
func writeJSONAtomic(path string, v any) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filestoreFilePerm)
	if err != nil {
		return fmt.Errorf("session: open tmp: %w", err)
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
		return fmt.Errorf("session: encode: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("session: fsync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("session: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("session: rename: %w", err)
	}
	return nil
}
