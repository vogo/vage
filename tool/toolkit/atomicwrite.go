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

package toolkit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

const (
	// DefaultFilePermission is the default permission for created files.
	DefaultFilePermission = 0o644
	// DefaultDirPermission is the default permission for created directories.
	DefaultDirPermission = 0o755
)

// AtomicWriteFile writes content to path atomically by writing to a temporary
// file in the same directory and then renaming it. Parent directories are
// created automatically. perm sets the file permissions on the final file.
func AtomicWriteFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DefaultDirPermission); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".atomic-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tmpPath := tmpFile.Name()

	_, writeErr := tmpFile.Write(content)
	closeErr := tmpFile.Close()

	if writeErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to write temp file: %w", writeErr)
	}

	if closeErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to close temp file: %w", closeErr)
	}

	if chmodErr := os.Chmod(tmpPath, perm); chmodErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to set file permissions: %w", chmodErr)
	}

	if renameErr := os.Rename(tmpPath, path); renameErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to rename temp file: %w", renameErr)
	}

	return nil
}

// AtomicWriteInRoot writes content to relPath inside root atomically via a
// temp file in the same directory followed by rename. relPath uses forward
// slashes (os.Root convention). Parent directories must already exist.
func AtomicWriteInRoot(root *os.Root, relPath string, content []byte, perm fs.FileMode) error {
	dir := path.Dir(relPath)
	if dir == "" {
		dir = "."
	}

	tmpName, err := randomTempName()
	if err != nil {
		return fmt.Errorf("failed to generate temp name: %w", err)
	}

	tmpBase := "." + filepath.Base(relPath) + ".tmp-" + tmpName

	var tmpRel string
	if dir == "." {
		tmpRel = tmpBase
	} else {
		tmpRel = dir + "/" + tmpBase
	}

	f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	_, writeErr := f.Write(content)
	closeErr := f.Close()

	if writeErr != nil {
		_ = root.Remove(tmpRel)

		return fmt.Errorf("failed to write temp file: %w", writeErr)
	}

	if closeErr != nil {
		_ = root.Remove(tmpRel)

		return fmt.Errorf("failed to close temp file: %w", closeErr)
	}

	if chmodErr := root.Chmod(tmpRel, perm); chmodErr != nil {
		_ = root.Remove(tmpRel)

		return fmt.Errorf("failed to set file permissions: %w", chmodErr)
	}

	if renameErr := root.Rename(tmpRel, relPath); renameErr != nil {
		_ = root.Remove(tmpRel)

		return fmt.Errorf("failed to rename temp file: %w", renameErr)
	}

	return nil
}

func randomTempName() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf[:]), nil
}
