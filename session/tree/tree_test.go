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

package tree

import (
	"strings"
	"testing"
	"time"
)

func TestNodeTypeValid(t *testing.T) {
	cases := map[NodeType]bool{
		NodeGoal:        true,
		NodeSubtask:     true,
		NodeFact:        true,
		NodeObservation: true,
		NodeArtifactRef: true,
		"":              false,
		"banana":        false,
	}
	for v, want := range cases {
		if got := v.Valid(); got != want {
			t.Errorf("NodeType(%q).Valid()=%v want %v", v, got, want)
		}
	}
}

func TestNodeStatusValid(t *testing.T) {
	cases := map[NodeStatus]bool{
		StatusPending:    true,
		StatusActive:     true,
		StatusDone:       true,
		StatusBlocked:    true,
		StatusSuperseded: true,
		"":               false,
		"abandoned":      false,
	}
	for v, want := range cases {
		if got := v.Valid(); got != want {
			t.Errorf("NodeStatus(%q).Valid()=%v want %v", v, got, want)
		}
	}
}

func TestValidateSessionID(t *testing.T) {
	cases := []struct {
		id   string
		want bool // true = valid
	}{
		{"abc", true},
		{"sess-2026.04.30_001", true},
		{"", false},
		{".", false},
		{"..", false},
		{"slash/in/id", false},
		{strings.Repeat("a", sessionIDMaxLen+1), false},
	}
	for _, c := range cases {
		err := validateSessionID(c.id)
		if (err == nil) != c.want {
			t.Errorf("validateSessionID(%q)=%v want valid=%v", c.id, err, c.want)
		}
	}
}

func TestValidateNodeID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"tn-12345-abcdef01", true},
		{"tn-x", true},
		{"foo", false},  // missing prefix
		{"tn-", false},  // body empty
		{"", false},
		{"tn-bad/slash", false},
	}
	for _, c := range cases {
		err := validateNodeID(c.id)
		if (err == nil) != c.want {
			t.Errorf("validateNodeID(%q)=%v want valid=%v", c.id, err, c.want)
		}
	}
}

func TestValidateNodePayload(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		n := TreeNode{Type: NodeGoal, Title: "OK"}
		if err := validateNodePayload(n); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("bad type", func(t *testing.T) {
		n := TreeNode{Type: "foo", Title: "OK"}
		if err := validateNodePayload(n); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty title", func(t *testing.T) {
		n := TreeNode{Type: NodeGoal}
		if err := validateNodePayload(n); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("title too long", func(t *testing.T) {
		n := TreeNode{Type: NodeGoal, Title: strings.Repeat("x", TitleMaxBytes+1)}
		if err := validateNodePayload(n); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("summary too long", func(t *testing.T) {
		n := TreeNode{
			Type:    NodeGoal,
			Title:   "OK",
			Summary: strings.Repeat("y", SummaryMaxBytes+1),
		}
		if err := validateNodePayload(n); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid status", func(t *testing.T) {
		n := TreeNode{Type: NodeGoal, Title: "OK", Status: "abandoned"}
		if err := validateNodePayload(n); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestGenerateNodeID(t *testing.T) {
	id := generateNodeID(time.Unix(1714400000, 12345))
	if !strings.HasPrefix(id, nodeIDPrefix) {
		t.Errorf("missing prefix: %s", id)
	}
	if err := validateNodeID(id); err != nil {
		t.Errorf("generated id failed validation: %v", err)
	}
}

func TestCloneNode(t *testing.T) {
	src := &TreeNode{
		ID:         "tn-x",
		Type:       NodeGoal,
		Status:     StatusActive,
		Title:      "T",
		Children:   []string{"a", "b"},
		Evidence:   []string{"e1"},
		Supersedes: []string{"old"},
		Metadata:   map[string]any{"k": 1},
	}
	dst := cloneNode(src)
	dst.Children[0] = "MUTATED"
	dst.Evidence[0] = "MUTATED"
	dst.Supersedes[0] = "MUTATED"
	dst.Metadata["k"] = "MUTATED"

	if src.Children[0] != "a" || src.Evidence[0] != "e1" || src.Supersedes[0] != "old" {
		t.Errorf("cloneNode shared slice state: src=%+v", src)
	}
	if v, _ := src.Metadata["k"].(int); v != 1 {
		t.Errorf("cloneNode shared map state: %v", src.Metadata)
	}
}

func TestCloneTree(t *testing.T) {
	root := &TreeNode{ID: "tn-r", Type: NodeGoal, Title: "T"}
	tr := &SessionTree{
		SessionID: "s",
		RootID:    "tn-r",
		Cursor:    "tn-r",
		Nodes:     map[string]*TreeNode{"tn-r": root},
	}
	c := cloneTree(tr)
	c.Nodes["tn-r"].Title = "MUTATED"
	if tr.Nodes["tn-r"].Title != "T" {
		t.Errorf("cloneTree shared node pointer: src=%q", tr.Nodes["tn-r"].Title)
	}
}
