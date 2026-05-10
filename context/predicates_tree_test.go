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

package vctx

import (
	"testing"

	"github.com/vogo/vage/session/tree/vectorhook"
	"github.com/vogo/vage/vector"
)

func docWithNodeID(id, nid string) vector.Document {
	return vector.Document{
		ID: id,
		Metadata: map[string]any{
			vectorhook.MetadataKeyNodeID: nid,
		},
	}
}

func TestNonPathNodesPredicate_NilOrEmptyPathReturnsNil(t *testing.T) {
	if NonPathNodesPredicate(nil) != nil {
		t.Errorf("nil path: expected nil predicate")
	}
	if NonPathNodesPredicate([]string{}) != nil {
		t.Errorf("empty path: expected nil predicate")
	}
	if NonPathNodesPredicate([]string{"", ""}) != nil {
		t.Errorf("empty-strings-only path: expected nil predicate")
	}
}

func TestNonPathNodesPredicate_DropsPathNodes(t *testing.T) {
	pred := NonPathNodesPredicate([]string{"tn-root", "tn-mid"})
	if pred == nil {
		t.Fatalf("nil predicate")
	}
	cases := []struct {
		nid  string
		keep bool
	}{
		{"tn-root", false},
		{"tn-mid", false},
		{"tn-leaf", true},
		{"", true}, // empty node_id ⇒ kept (not a tree doc)
	}
	for _, c := range cases {
		got := pred(docWithNodeID("d-"+c.nid, c.nid))
		if got != c.keep {
			t.Errorf("nid=%q: pred=%v, want %v", c.nid, got, c.keep)
		}
	}
}

func TestNonPathNodesPredicate_KeepsDocsWithoutNodeID(t *testing.T) {
	pred := NonPathNodesPredicate([]string{"tn-x"})
	d := vector.Document{
		ID: "agent-end-doc",
		Metadata: map[string]any{
			"session_id": "sess",
			// no node_id
		},
	}
	if !pred(d) {
		t.Errorf("doc without node_id should be kept (predicate is tree-scoped)")
	}
}

func TestNonPathNodesPredicate_NonStringNodeID_Kept(t *testing.T) {
	// Defensive: a producer accidentally writing an int into node_id
	// metadata should not crash the filter. We treat it as "not a
	// tree doc" and keep.
	pred := NonPathNodesPredicate([]string{"tn-x"})
	d := vector.Document{
		ID: "weird",
		Metadata: map[string]any{
			vectorhook.MetadataKeyNodeID: 42, // wrong type
		},
	}
	if !pred(d) {
		t.Errorf("non-string node_id should be kept")
	}
}
