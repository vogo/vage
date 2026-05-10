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
)

// scratchDirName is the on-disk subtree under workspace/ that holds
// scratch slots. Kept private — the on-disk shape is an implementation
// detail; the public surface is the Workspace interface.
const scratchDirName = "scratch"

// scratchFileSuffix mirrors noteFileSuffix: scratch entries are
// markdown so editors render them. Keeping the suffix consistent
// avoids surprise when a user inspects the sandbox manually.
const scratchFileSuffix = ".md"

func (w *FileWorkspace) scratchSlotDir(id, slot string) string {
	return filepath.Join(w.sessionDir(id), scratchDirName, slot)
}

func (w *FileWorkspace) scratchPath(id, slot, name string) string {
	return filepath.Join(w.scratchSlotDir(id, slot), name+scratchFileSuffix)
}

// WriteScratch implements Workspace.WriteScratch.
func (w *FileWorkspace) WriteScratch(ctx context.Context, sessionID, slot, name, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateSlotName(slot); err != nil {
		return err
	}
	if err := validateNoteName(name); err != nil {
		return err
	}
	if len(content) > MaxScratchBytesPerFile {
		return fmt.Errorf("%w: scratch %q size %d exceeds %d",
			ErrTooLarge, name, len(content), MaxScratchBytesPerFile)
	}

	mu := w.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	path := w.scratchPath(sessionID, slot, name)
	if content == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace: remove scratch: %w", err)
		}
		return nil
	}

	// Cap check applies only to *new* entries; updating an existing one
	// is always allowed. Performed under the lock so we cannot race past
	// the cap.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		count, listErr := w.countScratchLocked(sessionID, slot)
		if listErr != nil {
			return listErr
		}
		if count >= MaxScratchFilesPerSlot {
			return fmt.Errorf("%w: %d entries already in slot %q",
				ErrTooManyScratch, count, slot)
		}
	} else if err != nil {
		return fmt.Errorf("workspace: stat scratch: %w", err)
	}

	if err := os.MkdirAll(w.scratchSlotDir(sessionID, slot), dirPerm); err != nil {
		return fmt.Errorf("workspace: create scratch dir: %w", err)
	}
	return writeFileAtomic(path, []byte(content))
}

// ReadScratch implements Workspace.ReadScratch.
func (w *FileWorkspace) ReadScratch(ctx context.Context, sessionID, slot, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	if err := validateSlotName(slot); err != nil {
		return "", err
	}
	if err := validateNoteName(name); err != nil {
		return "", err
	}

	data, err := os.ReadFile(w.scratchPath(sessionID, slot, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("workspace: read scratch: %w", err)
	}
	return string(data), nil
}

// ListScratch implements Workspace.ListScratch.
func (w *FileWorkspace) ListScratch(ctx context.Context, sessionID, slot string) ([]NoteInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := validateSlotName(slot); err != nil {
		return nil, err
	}

	dir := w.scratchSlotDir(sessionID, slot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []NoteInfo{}, nil
		}
		return nil, fmt.Errorf("workspace: read scratch dir: %w", err)
	}

	out := make([]NoteInfo, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		base, suffix := splitNoteFile(ent.Name())
		if suffix != scratchFileSuffix || base == "" {
			continue
		}
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

// DeleteScratchSlot implements Workspace.DeleteScratchSlot.
func (w *FileWorkspace) DeleteScratchSlot(ctx context.Context, sessionID, slot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateSlotName(slot); err != nil {
		return err
	}

	mu := w.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	if err := os.RemoveAll(w.scratchSlotDir(sessionID, slot)); err != nil {
		return fmt.Errorf("workspace: delete scratch slot: %w", err)
	}
	return nil
}

// countScratchLocked counts current entries in a scratch slot. Caller
// must hold the per-session lock. Missing slot dir counts as zero.
func (w *FileWorkspace) countScratchLocked(sessionID, slot string) (int, error) {
	entries, err := os.ReadDir(w.scratchSlotDir(sessionID, slot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("workspace: count scratch: %w", err)
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		base, suffix := splitNoteFile(ent.Name())
		if suffix != scratchFileSuffix || base == "" {
			continue
		}
		if validateNoteName(base) != nil {
			continue
		}
		n++
	}
	return n, nil
}
