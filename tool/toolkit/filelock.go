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

import "sync"

type pathLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var globalLocker = &pathLocker{locks: make(map[string]*sync.Mutex)} //nolint:gochecknoglobals // process-level file lock registry

// LockPath acquires a process-level mutex for the given file path.
// It prevents concurrent read-modify-write races on the same file within a
// single process. Returns an unlock function that must be called when done.
func LockPath(path string) func() {
	globalLocker.mu.Lock()

	l, ok := globalLocker.locks[path]
	if !ok {
		l = &sync.Mutex{}
		globalLocker.locks[path] = l
	}

	globalLocker.mu.Unlock()

	l.Lock()

	return l.Unlock
}
