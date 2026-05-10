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

package edit

import (
	"testing"

	"github.com/vogo/vage/tool"
)

func TestEditTool_ResourceIDs_HappyPath(t *testing.T) {
	et := New()
	got := et.ResourceIDs(map[string]any{
		"file_path":  "/abs/path/to/file.go",
		"old_string": "foo",
		"new_string": "bar",
	})

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// edit treats the file as overwritten — though it depends on a
	// prior read, the stale-resource pass needs to see edit as the
	// invalidator of any earlier read tool_result.
	want := tool.ResourceRef{ID: "/abs/path/to/file.go", Mode: tool.ResourceWrite}
	if got[0] != want {
		t.Errorf("ref = %+v, want %+v", got[0], want)
	}
}

func TestEditTool_ResourceIDs_CleansPath(t *testing.T) {
	et := New()
	got := et.ResourceIDs(map[string]any{"file_path": "/abs/./a//b/../c.go"})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	const want = "/abs/a/c.go"
	if got[0].ID != want {
		t.Errorf("ID = %q, want %q", got[0].ID, want)
	}
}

func TestEditTool_ResourceIDs_Defensive(t *testing.T) {
	et := New()
	cases := []struct {
		name string
		args map[string]any
	}{
		{"nil args", nil},
		{"empty args", map[string]any{}},
		{"empty string", map[string]any{"file_path": ""}},
		{"wrong type int", map[string]any{"file_path": 42}},
		{"wrong type slice", map[string]any{"file_path": []string{"a"}}},
		{"wrong type nil", map[string]any{"file_path": nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := et.ResourceIDs(tc.args); got != nil {
				t.Errorf("got %+v, want nil", got)
			}
		})
	}
}
