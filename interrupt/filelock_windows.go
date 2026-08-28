//go:build windows

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

package interrupt

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockAllBytes covers the whole (possibly empty) file: the lock is a pure
// mutex, the file's contents are never read.
const lockAllBytes = ^uint32(0)

// tryLockFile / unlockFile are the Windows counterpart of the flock-based
// implementation; see filelock_unix.go for the ownership property both
// provide. LockFileEx locks are held by the file handle and released by the
// kernel when the handle closes, including on process death.
func tryLockFile(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockAllBytes, lockAllBytes, &ol,
	)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		// Held by someone else: the caller retries.
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockAllBytes, lockAllBytes, &ol)
}
