//go:build !windows

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
	"syscall"
)

// tryLockFile / unlockFile wrap the platform's advisory whole-file lock.
//
// The property FileStore depends on — and the reason this is not a
// hand-rolled lock file — is that the kernel owns the lock: it is released
// when the holder unlocks it or when its file descriptor is closed, which
// includes the process dying for any reason. A holder can therefore never
// be preempted by a bystander that merely thinks it has waited long
// enough, and a crashed holder never leaves the record wedged.
//
// flock is per open file description, so two FileStore instances in one
// process contend exactly as two processes do.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EINTR):
		// Held by someone else (or interrupted): the caller retries.
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
