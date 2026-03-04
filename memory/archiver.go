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

package memory

import "context"

// Archiver decides which session memory entries are archived to store.
type Archiver interface {
	Archive(ctx context.Context, entries []Entry) ([]Entry, error)
}

// ArchiveFunc is a function adapter for Archiver.
type ArchiveFunc func(ctx context.Context, entries []Entry) ([]Entry, error)

// Archive implements Archiver.
func (f ArchiveFunc) Archive(ctx context.Context, entries []Entry) ([]Entry, error) {
	return f(ctx, entries)
}

// ArchiveAll returns an archiver that archives all entries.
func ArchiveAll() Archiver {
	return ArchiveFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		return entries, nil
	})
}

// ArchiveNone returns an archiver that archives no entries.
func ArchiveNone() Archiver {
	return ArchiveFunc(func(_ context.Context, _ []Entry) ([]Entry, error) {
		return nil, nil
	})
}
