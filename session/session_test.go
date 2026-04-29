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
	"errors"
	"strings"
	"testing"
)

func TestNew_PanicsOnEmptyID(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on empty id")
		}
		if !strings.Contains(string(asString(r)), "non-empty id") {
			t.Fatalf("unexpected panic message: %v", r)
		}
	}()
	_ = New("")
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		return "" // panics carry strings or errors in this package
	}
}

func TestNew_DefaultsActiveAndStamps(t *testing.T) {
	s := New("ok")
	if s.State != StateActive {
		t.Fatalf("expected Active, got %q", s.State)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not seeded: %+v", s)
	}
}

func TestGenerateID_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1024)
	prev := ""
	for range 1024 {
		id := GenerateID()
		if !IDPattern.MatchString(id) {
			t.Fatalf("generated id %q does not match IDPattern", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id: %q", id)
		}
		seen[id] = struct{}{}

		// Sortable by time: not strict equality, but successive calls should
		// be lexicographically non-decreasing as a prefix.
		if prev != "" && id <= prev {
			// Tolerate equal prefix with different random suffix.
			pPrefix := strings.SplitN(prev, "-", 2)[0]
			iPrefix := strings.SplitN(id, "-", 2)[0]
			if iPrefix < pPrefix {
				t.Fatalf("id not monotonic: %q after %q", id, prev)
			}
		}
		prev = id
	}
}

func TestValidateID(t *testing.T) {
	good := []string{"abc", "a.b.c", "_-_", "0", strings.Repeat("a", IDMaxLen)}
	for _, id := range good {
		if err := validateID(id); err != nil {
			t.Errorf("expected %q valid, got %v", id, err)
		}
	}
	bad := []string{"", "a/b", "a b", "中文", "a\nb", strings.Repeat("a", IDMaxLen+1)}
	for _, id := range bad {
		if err := validateID(id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("expected %q invalid, got %v", id, err)
		}
	}
}

func TestCloneSession_DeepEnough(t *testing.T) {
	s := New("c")
	s.Metadata = map[string]any{"k": "v"}
	cp := cloneSession(s)
	cp.Metadata["k"] = "x"
	if s.Metadata["k"] != "v" {
		t.Fatalf("metadata leaked: %v", s.Metadata)
	}
}
