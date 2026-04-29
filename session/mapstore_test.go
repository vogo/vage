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
	"context"
	"testing"
)

// MapStore-specific (not covered by conformance) coverage: insertion order
// for List.

func TestMapStore_ListInsertionOrder(t *testing.T) {
	st := NewMapSessionStore()
	for _, id := range []string{"c", "a", "b"} {
		if err := st.Create(context.Background(), New(id)); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	got, err := st.List(context.Background(), SessionFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"c", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: %d vs %d", len(got), len(want))
	}
	for i, s := range got {
		if s.ID != want[i] {
			t.Errorf("idx %d: want %q got %q", i, want[i], s.ID)
		}
	}
}
