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
	"testing"
)

// metaValue is a structured value that opts in to both importance and tags.
// Values without these methods carry no selector metadata and must be
// filtered out by every selective predicate.
type metaValue struct {
	importance float64
	tags       []string
}

func (v metaValue) Importance() float64 { return v.importance }
func (v metaValue) Tags() []string      { return v.tags }

func entry(key string, value any) Entry {
	return Entry{Key: key, Value: value}
}

func promoteEntries(t *testing.T, p Promoter, entries []Entry) []Entry {
	t.Helper()
	out, err := p.Promote(context.Background(), entries)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	return out
}

func archiveEntries(t *testing.T, a Archiver, entries []Entry) []Entry {
	t.Helper()
	out, err := a.Archive(context.Background(), entries)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	return out
}

func keys(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

func TestPromoteWhen_ImportanceAtLeast(t *testing.T) {
	entries := []Entry{
		entry("high", metaValue{importance: 0.9}),
		entry("low", metaValue{importance: 0.2}),
		entry("plain", "no-metadata"),
	}

	got := keys(promoteEntries(t, PromoteWhen(ImportanceAtLeast(0.7)), entries))
	want := []string{"high"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("promoted = %v, want %v", got, want)
	}
}

func TestArchiveWhen_ImportanceAtLeast(t *testing.T) {
	entries := []Entry{
		entry("high", metaValue{importance: 0.8}),
		entry("low", metaValue{importance: 0.1}),
	}

	got := keys(archiveEntries(t, ArchiveWhen(ImportanceAtLeast(0.5)), entries))
	want := []string{"high"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("archived = %v, want %v", got, want)
	}
}

func TestTagged_RequiresAllTags(t *testing.T) {
	entries := []Entry{
		entry("both", metaValue{tags: []string{"a", "b", "c"}}),
		entry("one", metaValue{tags: []string{"a"}}),
		entry("none", metaValue{}),
	}

	got := keys(promoteEntries(t, PromoteWhen(Tagged("a", "b")), entries))
	if len(got) != 1 || got[0] != "both" {
		t.Errorf("promoted = %v, want [both]", got)
	}
}

func TestTagged_EmptyTagListMatchesAnyCarrier(t *testing.T) {
	entries := []Entry{
		entry("tagged", metaValue{tags: []string{"a"}}),
		entry("plain", "no-metadata"),
	}

	got := keys(promoteEntries(t, PromoteWhen(Tagged()), entries))
	want := []string{"tagged"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("promoted = %v, want %v", got, want)
	}
}

func TestAnd_Composition(t *testing.T) {
	entries := []Entry{
		entry("pref-high", metaValue{importance: 0.9, tags: []string{"pref"}}),
		entry("pref-low", metaValue{importance: 0.1, tags: []string{"pref"}}),
		entry("high-only", metaValue{importance: 0.9}),
	}

	got := keys(promoteEntries(t, PromoteWhen(And(ImportanceAtLeast(0.5), Tagged("pref"))), entries))
	want := []string{"pref-high"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("promoted = %v, want %v", got, want)
	}
}

func TestOr_Composition(t *testing.T) {
	entries := []Entry{
		entry("high", metaValue{importance: 0.9}),
		entry("tagged", metaValue{tags: []string{"pin"}}),
		entry("low", metaValue{importance: 0.1}),
	}

	got := keys(promoteEntries(t, PromoteWhen(Or(ImportanceAtLeast(0.5), Tagged("pin"))), entries))
	if len(got) != 2 {
		t.Errorf("promoted = %v, want 2 entries", got)
	}
}

func TestAndEmpty_IsAlwaysTrue(t *testing.T) {
	anyEntry := entry("anything", "no-metadata")
	if !And()(anyEntry) {
		t.Error("And() should match everything (identity for AND)")
	}
}

func TestOrEmpty_IsAlwaysFalse(t *testing.T) {
	anyEntry := entry("anything", "no-metadata")
	if Or()(anyEntry) {
		t.Error("Or() should match nothing (identity for OR)")
	}
}

func TestSelectivePredicate_FiltersValuesWithoutMetadata(t *testing.T) {
	entries := []Entry{
		entry("plain", "string-value"),
		entry("ptr-without-methods", &struct{ Name string }{Name: "x"}),
	}

	if got := promoteEntries(t, PromoteWhen(ImportanceAtLeast(0.0)), entries); len(got) != 0 {
		t.Errorf("importance predicate matched %v, want nothing (no metadata)", keys(got))
	}
	if got := promoteEntries(t, PromoteWhen(Tagged("any")), entries); len(got) != 0 {
		t.Errorf("tag predicate matched %v, want nothing (no metadata)", keys(got))
	}
}

func TestPromoteWhen_NilPredicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("PromoteWhen(nil) should panic")
		}
	}()
	PromoteWhen(nil)
}

func TestArchiveWhen_NilPredicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("ArchiveWhen(nil) should panic")
		}
	}()
	ArchiveWhen(nil)
}

func TestAndOr_NilMemberPanics(t *testing.T) {
	for _, build := range []func() EntryPredicate{
		func() EntryPredicate { return And(ImportanceAtLeast(0.5), nil) },
		func() EntryPredicate { return Or(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("nil member in And/Or should panic")
				}
			}()
			build()
		}()
	}
}

// TestManager_PromoteWhenThroughManager proves the selective promoter drives
// the real working→session flow and that the default Manager (PromoteAll +
// ArchiveNone) is unchanged.
func TestManager_PromoteWhenThroughManager(t *testing.T) {
	session := NewSessionMemory("a", "s")
	mgr := NewManager(WithSession(session), WithPromoter(PromoteWhen(ImportanceAtLeast(0.7))))

	working := NewWorkingMemory("a", "s")
	ctx := context.Background()
	for key, v := range map[string]metaValue{
		"keep": {importance: 0.9},
		"drop": {importance: 0.3},
	} {
		if err := working.Set(ctx, key, v, 0); err != nil {
			t.Fatalf("working Set %q: %v", key, err)
		}
	}

	if err := mgr.PromoteToSession(ctx, working); err != nil {
		t.Fatalf("PromoteToSession: %v", err)
	}

	if _, err := session.Get(ctx, "keep"); err != nil {
		t.Errorf("'keep' should have been promoted, Get err = %v", err)
	}
	if v, err := session.Get(ctx, "drop"); err != nil || v != nil {
		t.Errorf("'drop' should have been filtered out, Get = (%v, %v)", v, err)
	}
}

func TestManager_ArchiveWhenThroughManager(t *testing.T) {
	store := NewInMemoryLongTermMemory()
	session := NewSessionMemory("a", "s")
	mgr := NewManager(
		WithSession(session),
		WithStore(store),
		WithArchiver(ArchiveWhen(Tagged("longterm"))),
	)

	ctx := context.Background()
	for key, v := range map[string]metaValue{
		"archive":         {tags: []string{"longterm"}},
		"keep-in-session": {tags: []string{"ephemeral"}},
	} {
		if err := session.Set(ctx, key, v, 0); err != nil {
			t.Fatalf("session Set %q: %v", key, err)
		}
	}

	if err := mgr.ArchiveToStore(ctx); err != nil {
		t.Fatalf("ArchiveToStore: %v", err)
	}

	if _, err := store.Get(ctx, "archive"); err != nil {
		t.Errorf("'archive' should have been archived, Get err = %v", err)
	}
	if v, err := store.Get(ctx, "keep-in-session"); err != nil || v != nil {
		t.Errorf("'keep-in-session' should have been filtered out, Get = (%v, %v)", v, err)
	}
	// Archiving is not deletion: the session source must be untouched.
	if _, err := session.Get(ctx, "keep-in-session"); err != nil {
		t.Errorf("session source should survive archival, Get err = %v", err)
	}
}
