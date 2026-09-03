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

import (
	"context"
	"slices"
	"strconv"
)

// EntryPredicate reports whether a memory entry qualifies for a selective
// promotion or archival step. Entries the predicate rejects are left where
// they are — promotion or archival is skipped for them, nothing is deleted.
type EntryPredicate func(Entry) bool

// PromoteWhen returns a promoter that promotes only the entries for which
// pred returns true. A nil pred panics: a selective promoter that filters
// everything is a configuration mistake, and failing at assembly is cheaper
// than discovering empty promotions at run time.
func PromoteWhen(pred EntryPredicate) Promoter {
	if pred == nil {
		panic("memory: promoter predicate must not be nil")
	}

	return PromoteFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		out := make([]Entry, 0, len(entries))
		for i := range entries {
			if pred(entries[i]) {
				out = append(out, entries[i])
			}
		}
		return out, nil
	})
}

// ArchiveWhen returns an archiver that archives only the entries for which
// pred returns true. A nil pred panics for the same reason as PromoteWhen.
func ArchiveWhen(pred EntryPredicate) Archiver {
	if pred == nil {
		panic("memory: archiver predicate must not be nil")
	}

	return ArchiveFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		out := make([]Entry, 0, len(entries))
		for i := range entries {
			if pred(entries[i]) {
				out = append(out, entries[i])
			}
		}
		return out, nil
	})
}

// ImportanceAtLeast returns a predicate that matches entries whose value
// reports an importance score of at least threshold.
//
// Metadata is read from the entry's value, not from Entry itself: a value
// implementing Importance() float64 is treated as carrying an importance
// score. A value that does not implement it has no score and is matched by no
// importance predicate — it is filtered out. Selective promoters and
// archivers therefore require the values they keep to opt in to carrying
// metadata.
func ImportanceAtLeast(threshold float64) EntryPredicate {
	return func(e Entry) bool {
		v, ok := e.Value.(interface{ Importance() float64 })
		return ok && v.Importance() >= threshold
	}
}

// Tagged returns a predicate that matches entries whose value carries all of
// the given tags.
//
// As with ImportanceAtLeast, metadata is read from the value: a value
// implementing Tags() []string carries tags. A value without Tags() has no
// tags and matches no Tagged predicate. Matching requires every given tag to
// be present (the tags are ANDed); combine Tagged calls with Or for any-of
// semantics.
func Tagged(tags ...string) EntryPredicate {
	return func(e Entry) bool {
		v, ok := e.Value.(interface{ Tags() []string })
		if !ok {
			return false
		}

		have := v.Tags()
		for _, want := range tags {
			found := slices.Contains(have, want)
			if !found {
				return false
			}
		}
		return true
	}
}

// And returns a predicate that matches an entry only when every member
// matches it. With no members it matches everything (the identity element for
// AND, which keeps And(ImportanceAtLeast(0.5)) and And() consistent under
// composition). A nil member panics at construction.
func And(preds ...EntryPredicate) EntryPredicate {
	for i, p := range preds {
		if p == nil {
			panic("memory: nil predicate at index " + strconv.Itoa(i) + " passed to And")
		}
	}

	return func(e Entry) bool {
		for _, p := range preds {
			if !p(e) {
				return false
			}
		}
		return true
	}
}

// Or returns a predicate that matches an entry when any member matches it.
// With no members it matches nothing (the identity element for OR). A nil
// member panics at construction.
func Or(preds ...EntryPredicate) EntryPredicate {
	for i, p := range preds {
		if p == nil {
			panic("memory: nil predicate at index " + strconv.Itoa(i) + " passed to Or")
		}
	}

	return func(e Entry) bool {
		for _, p := range preds {
			if p(e) {
				return true
			}
		}
		return false
	}
}
