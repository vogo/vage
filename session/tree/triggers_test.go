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
)

func mkChild(status NodeStatus, titleSize, summarySize int) *TreeNode {
	return &TreeNode{
		Type:    NodeSubtask,
		Status:  status,
		Title:   strings.Repeat("t", titleSize),
		Summary: strings.Repeat("s", summarySize),
	}
}

func TestChildrenCountDecider(t *testing.T) {
	d := ChildrenCountDecider{Min: 3}
	if d.ShouldPromote(nil, []*TreeNode{{}, {}}) {
		t.Error("2 < 3 should not fire")
	}
	if !d.ShouldPromote(nil, []*TreeNode{{}, {}, {}}) {
		t.Error("3 >= 3 should fire")
	}
}

func TestChildrenCountDecider_Default(t *testing.T) {
	d := ChildrenCountDecider{} // 0 → DefaultPromotionMinChildren
	smaller := make([]*TreeNode, DefaultPromotionMinChildren-1)
	for i := range smaller {
		smaller[i] = &TreeNode{}
	}
	if d.ShouldPromote(nil, smaller) {
		t.Error("below default should not fire")
	}
	bigger := append(smaller, &TreeNode{})
	if !d.ShouldPromote(nil, bigger) {
		t.Error("at default should fire")
	}
}

func TestAllChildrenDoneDecider(t *testing.T) {
	d := AllChildrenDoneDecider{}
	if d.ShouldPromote(nil, nil) {
		t.Error("no children should not fire")
	}
	if d.ShouldPromote(nil, []*TreeNode{{Status: StatusActive}}) {
		t.Error("active child should not fire")
	}
	if !d.ShouldPromote(nil, []*TreeNode{
		{Status: StatusDone}, {Status: StatusDone},
	}) {
		t.Error("all done should fire")
	}
	if d.ShouldPromote(nil, []*TreeNode{
		{Status: StatusDone}, {Status: StatusBlocked},
	}) {
		t.Error("mixed should not fire")
	}
}

func TestSubtreeBytesDecider(t *testing.T) {
	d := SubtreeBytesDecider{Min: 100}
	if d.ShouldPromote(nil, []*TreeNode{mkChild(StatusDone, 10, 10)}) {
		t.Error("under threshold should not fire")
	}
	if !d.ShouldPromote(nil, []*TreeNode{
		mkChild(StatusDone, 50, 50), mkChild(StatusDone, 1, 1),
	}) {
		t.Error("at threshold should fire")
	}
}

func TestSubtreeBytesDecider_Default(t *testing.T) {
	d := SubtreeBytesDecider{}
	huge := mkChild(StatusDone, TitleMaxBytes, SummaryMaxBytes)
	// One node is at most 200+2048 = 2248 bytes; need ~4 to cross 8 KiB.
	if d.ShouldPromote(nil, []*TreeNode{huge}) {
		t.Error("single node should not exceed default 8 KiB")
	}
	many := []*TreeNode{huge, huge, huge, huge, huge}
	if !d.ShouldPromote(nil, many) {
		t.Error("five max-sized nodes should cross default")
	}
}

func TestAnyOf(t *testing.T) {
	never := DeciderFunc(func(*TreeNode, []*TreeNode) bool { return false })
	always := DeciderFunc(func(*TreeNode, []*TreeNode) bool { return true })
	if AnyOf().ShouldPromote(nil, nil) {
		t.Error("empty AnyOf should not fire")
	}
	if AnyOf(never, never).ShouldPromote(nil, nil) {
		t.Error("AnyOf(never, never) should not fire")
	}
	if !AnyOf(never, always).ShouldPromote(nil, nil) {
		t.Error("AnyOf(never, always) should fire")
	}
	// nil decider must be tolerated.
	if !AnyOf(nil, always).ShouldPromote(nil, nil) {
		t.Error("nil decider should be skipped, not panic")
	}
}

func TestAllOf(t *testing.T) {
	never := DeciderFunc(func(*TreeNode, []*TreeNode) bool { return false })
	always := DeciderFunc(func(*TreeNode, []*TreeNode) bool { return true })
	if !AllOf().ShouldPromote(nil, nil) {
		t.Error("empty AllOf should fire (vacuously true)")
	}
	if !AllOf(always, always).ShouldPromote(nil, nil) {
		t.Error("AllOf(always, always) should fire")
	}
	if AllOf(always, never).ShouldPromote(nil, nil) {
		t.Error("AllOf(always, never) should not fire")
	}
	if !AllOf(nil, always).ShouldPromote(nil, nil) {
		t.Error("nil decider should be skipped")
	}
}

func TestEligibleChildren(t *testing.T) {
	tr := &SessionTree{Nodes: map[string]*TreeNode{
		"a": {ID: "a"},
		"b": {ID: "b", Pinned: true},
		"c": {ID: "c", Promoted: true},
		"d": {ID: "d"},
	}}
	parent := &TreeNode{ID: "p", Children: []string{"a", "b", "c", "d", "missing"}}
	out := eligibleChildren(tr, parent)
	if len(out) != 2 {
		t.Fatalf("eligible=%d want 2 (a,d)", len(out))
	}
	if out[0].ID != "a" || out[1].ID != "d" {
		t.Errorf("order/content wrong: %+v", out)
	}
	// Nil tree / nil parent must return nil safely.
	if eligibleChildren(nil, parent) != nil {
		t.Error("nil tree should return nil")
	}
	if eligibleChildren(tr, nil) != nil {
		t.Error("nil parent should return nil")
	}
}

func TestPromotionInflight(t *testing.T) {
	var p promotionInflight
	if !p.reserve("s", "n") {
		t.Error("first reserve should succeed")
	}
	if p.reserve("s", "n") {
		t.Error("duplicate reserve should fail")
	}
	if !p.reserve("s", "other") {
		t.Error("different node should succeed")
	}
	p.release("s", "n")
	if !p.reserve("s", "n") {
		t.Error("after release the slot should be free")
	}
	// Idempotent release on unknown key.
	p.release("never", "claimed")
}
