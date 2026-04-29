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
	"errors"
	"strings"
	"testing"
)

// TestValidateNoteName_RejectsAttacks asserts the path-traversal & malformed
// names rejected by validateNoteName. These are also the vectors mirrored in
// the workspace tools that the LLM can call.
func TestValidateNoteName_RejectsAttacks(t *testing.T) {
	cases := []struct {
		name string
		why  string
	}{
		{"", "empty"},
		{".", "dot — path traversal"},
		{"..", "double-dot — parent dir"},
		{"../passwd", "leading ../"},
		{"foo/bar", "embedded slash"},
		{"foo\\bar", "embedded backslash"},
		{"with space", "space"},
		{"name\x00null", "embedded NUL"},
		{strings.Repeat("a", NoteNameMaxLen+1), "over length cap"},
	}
	for _, tc := range cases {
		err := validateNoteName(tc.name)
		if err == nil {
			t.Errorf("validateNoteName(%q) = nil, want error (%s)", tc.name, tc.why)
			continue
		}
		if !errors.Is(err, ErrInvalidName) {
			t.Errorf("validateNoteName(%q) = %v, want ErrInvalidName (%s)", tc.name, err, tc.why)
		}
	}
}

// TestValidateNoteName_AcceptsLegitimate covers names the LLM is expected to
// pick: simple identifiers, dotted compound names, dashes and underscores.
func TestValidateNoteName_AcceptsLegitimate(t *testing.T) {
	cases := []string{
		"plan",
		"design.notes",
		"refs-to-check",
		"v2_decisions",
		"a",                                 // single char
		strings.Repeat("a", NoteNameMaxLen), // exact cap
	}
	for _, name := range cases {
		if err := validateNoteName(name); err != nil {
			t.Errorf("validateNoteName(%q) = %v, want nil", name, err)
		}
	}
}

// TestValidateSessionID_RejectsAttacks mirrors the session-id rejections so
// the workspace cannot be tricked through a forged session id either.
func TestValidateSessionID_RejectsAttacks(t *testing.T) {
	cases := []string{
		"",
		".",
		"..",
		"foo/bar",
		"foo\\bar",
		"id with space",
		strings.Repeat("a", 129), // over IDMaxLen
	}
	for _, id := range cases {
		if err := validateSessionID(id); err == nil {
			t.Errorf("validateSessionID(%q) = nil, want error", id)
		} else if !errors.Is(err, ErrInvalidSession) {
			t.Errorf("validateSessionID(%q) = %v, want ErrInvalidSession", id, err)
		}
	}
}

// TestSplitNoteFile checks the suffix-aware splitter used by ListNotes /
// countNotesLocked. The returned suffix includes the leading dot so callers
// can compare it directly to noteFileSuffix.
func TestSplitNoteFile(t *testing.T) {
	cases := []struct {
		in         string
		wantBase   string
		wantSuffix string
	}{
		{"plan.md", "plan", ".md"},
		{"a.b.md", "a.b", ".md"},
		{"plan", "plan", ""},
		{".hidden", "", ".hidden"},
		{"", "", ""},
	}
	for _, tc := range cases {
		gotBase, gotSuffix := splitNoteFile(tc.in)
		if gotBase != tc.wantBase || gotSuffix != tc.wantSuffix {
			t.Errorf("splitNoteFile(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotBase, gotSuffix, tc.wantBase, tc.wantSuffix)
		}
	}
}
