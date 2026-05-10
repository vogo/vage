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

package read

import (
	"testing"

	"github.com/vogo/vage/tool"
)

func TestReadTool_ResourceIDs_HappyPath(t *testing.T) {
	rt := New()
	got := rt.ResourceIDs(map[string]any{"file_path": "/abs/path/to/file.go"})

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	want := tool.ResourceRef{ID: "/abs/path/to/file.go", Mode: tool.ResourceRead}
	if got[0] != want {
		t.Errorf("ref = %+v, want %+v", got[0], want)
	}
}

func TestReadTool_ResourceIDs_CleansPath(t *testing.T) {
	rt := New()
	// filepath.Clean collapses "./" and resolves "..", but does not
	// touch the leading slash. Pin the canonical form so a downstream
	// stale-resource map keys consistently regardless of how the LLM
	// spelled the path.
	got := rt.ResourceIDs(map[string]any{"file_path": "/abs/./a//b/../c.go"})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	const want = "/abs/a/c.go"
	if got[0].ID != want {
		t.Errorf("ID = %q, want %q", got[0].ID, want)
	}
}

func TestReadTool_ResourceIDs_MissingPath(t *testing.T) {
	rt := New()
	if got := rt.ResourceIDs(map[string]any{}); got != nil {
		t.Errorf("missing file_path returned %+v, want nil", got)
	}
}

func TestReadTool_ResourceIDs_WrongType(t *testing.T) {
	rt := New()
	cases := []map[string]any{
		{"file_path": 42},
		{"file_path": []string{"a"}},
		{"file_path": nil},
	}
	for i, args := range cases {
		if got := rt.ResourceIDs(args); got != nil {
			t.Errorf("case %d: got %+v, want nil", i, got)
		}
	}
}

func TestReadTool_ResourceIDs_EmptyString(t *testing.T) {
	rt := New()
	if got := rt.ResourceIDs(map[string]any{"file_path": ""}); got != nil {
		t.Errorf("empty file_path returned %+v, want nil", got)
	}
}

func TestReadTool_ResourceIDs_NilArgs(t *testing.T) {
	rt := New()
	// Defensive: ResourceIDs must not panic when handed a nil map.
	if got := rt.ResourceIDs(nil); got != nil {
		t.Errorf("nil args returned %+v, want nil", got)
	}
}
