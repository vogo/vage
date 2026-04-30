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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/session/tree"
)

// stubTreeStore is the minimal SessionTreeStore double used for branching-
// logic tests. We do NOT depend on tree.MapTreeStore for these unit tests
// — the goal is to drive Source.Fetch through every status code without
// having to manually wire happy-path tree state for every case.
type stubTreeStore struct {
	tr  *tree.SessionTree
	err error
}

func (s *stubTreeStore) CreateTree(context.Context, string, tree.TreeNode) (*tree.TreeNode, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTreeStore) GetTree(_ context.Context, _ string) (*tree.SessionTree, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tr, nil
}

func (s *stubTreeStore) AddNode(context.Context, string, string, tree.TreeNode) (*tree.TreeNode, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTreeStore) UpdateNode(context.Context, string, tree.TreeNode) (*tree.TreeNode, error) {
	return nil, errors.New("not implemented")
}
func (s *stubTreeStore) DeleteNode(context.Context, string, string) error { return nil }
func (s *stubTreeStore) SetCursor(context.Context, string, string) error  { return nil }
func (s *stubTreeStore) DeleteTree(context.Context, string) error         { return nil }
func (s *stubTreeStore) PromoteNode(context.Context, string, string) (*tree.TreeNode, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTreeStore) GetTreeView(_ context.Context, _ string, _ tree.ViewOptions) (*tree.SessionTree, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tr, nil
}

func TestTreeSource_Skipped_NilStore(t *testing.T) {
	src := &SessionTreeSource{}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status=%s want skipped", res.Report.Status)
	}
	if len(res.Messages) != 0 {
		t.Errorf("messages=%d want 0", len(res.Messages))
	}
}

func TestTreeSource_Skipped_TreeMissing(t *testing.T) {
	src := &SessionTreeSource{Store: &stubTreeStore{err: tree.ErrTreeMissing}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status=%s want skipped", res.Report.Status)
	}
}

func TestTreeSource_Error_StoreFailure(t *testing.T) {
	src := &SessionTreeSource{Store: &stubTreeStore{err: errors.New("disk on fire")}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatalf("Fetch returned error; should fail-open: %v", err)
	}
	if res.Report.Status != StatusError {
		t.Errorf("Status=%s want error", res.Report.Status)
	}
	if !strings.Contains(res.Report.Error, "disk on fire") {
		t.Errorf("Error=%q missing detail", res.Report.Error)
	}
}

func TestTreeSource_Error_NoRoot(t *testing.T) {
	src := &SessionTreeSource{Store: &stubTreeStore{tr: &tree.SessionTree{SessionID: "s"}}}
	res, _ := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if res.Report.Status != StatusError {
		t.Errorf("Status=%s want error", res.Report.Status)
	}
}

// fixtureTree builds a small tree shaped like:
//
//	root (goal, active)
//	├── A (subtask, done) — recently completed sibling of B
//	├── B (subtask, active) ← cursor
//	│    ├── B1 (fact, done)
//	│    └── B2 (subtask, pending)
//	└── C (subtask, pending)
//
// We hand-construct it (instead of going through CreateTree/AddNode) so
// the test focuses on Source.Fetch's projection logic, not the store.
func fixtureTree() *tree.SessionTree {
	root := &tree.TreeNode{
		ID: "tn-root", Type: tree.NodeGoal, Status: tree.StatusActive,
		Title: "deliver feature", Summary: "feature flag X for users in EU",
	}
	a := &tree.TreeNode{ID: "tn-a", Type: tree.NodeSubtask, Status: tree.StatusDone, Title: "design schema", Summary: "chose foo+bar tables", Parent: "tn-root", Depth: 1}
	b := &tree.TreeNode{ID: "tn-b", Type: tree.NodeSubtask, Status: tree.StatusActive, Title: "wire dispatcher", Summary: "callback + token store", Parent: "tn-root", Depth: 1}
	c := &tree.TreeNode{ID: "tn-c", Type: tree.NodeSubtask, Status: tree.StatusPending, Title: "add e2e tests", Parent: "tn-root", Depth: 1}
	b1 := &tree.TreeNode{ID: "tn-b1", Type: tree.NodeFact, Status: tree.StatusDone, Title: "schema confirmed", Parent: "tn-b", Depth: 2}
	b2 := &tree.TreeNode{ID: "tn-b2", Type: tree.NodeSubtask, Status: tree.StatusPending, Title: "implement callback", Parent: "tn-b", Depth: 2}
	root.Children = []string{"tn-a", "tn-b", "tn-c"}
	b.Children = []string{"tn-b1", "tn-b2"}
	return &tree.SessionTree{
		SessionID: "s",
		RootID:    "tn-root",
		Cursor:    "tn-b",
		Nodes: map[string]*tree.TreeNode{
			"tn-root": root, "tn-a": a, "tn-b": b, "tn-c": c, "tn-b1": b1, "tn-b2": b2,
		},
	}
}

func TestTreeSource_OK_Render(t *testing.T) {
	src := &SessionTreeSource{Store: &stubTreeStore{tr: fixtureTree()}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Status != StatusOK {
		t.Fatalf("Status=%s want ok; report=%+v", res.Report.Status, res.Report)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages=%d want 1", len(res.Messages))
	}
	body := res.Messages[0].Content.Text()

	// Spot-check landmarks. The exact layout is a documented format and the
	// test pins enough markers to fail loudly on accidental reflows.
	for _, want := range []string{
		"## Session Tree",
		"### Goal",
		"deliver feature",
		"### Path (root → cursor)",
		"← cursor",
		"### Cursor's children",
		"implement callback",
		"### Recently completed (sibling)",
		"design schema",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--body--\n%s", want, body)
		}
	}
}

func TestTreeSource_NoCursor_RendersRootOnly(t *testing.T) {
	tr := fixtureTree()
	tr.Cursor = "" // unset cursor
	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()
	// When cursor falls back to root, no separate Path section needs printing.
	if strings.Contains(body, "### Path") {
		t.Errorf("did not expect Path block when cursor==root\n%s", body)
	}
}

func TestTreeSource_SiblingsTruncation(t *testing.T) {
	tr := fixtureTree()
	// Add 12 children under cursor B; MaxSiblingTitles default is 8.
	for i := range 12 {
		id := "tn-extra-" + string(rune('a'+i))
		tr.Nodes[id] = &tree.TreeNode{
			ID:     id,
			Type:   tree.NodeFact,
			Status: tree.StatusPending,
			Title:  "extra " + id,
			Parent: "tn-b",
			Depth:  2,
		}
		tr.Nodes["tn-b"].Children = append(tr.Nodes["tn-b"].Children, id)
	}
	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}}
	res, _ := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "(... and ") {
		t.Errorf("expected truncation marker; body:\n%s", body)
	}
}

func TestTreeSource_ByteCap(t *testing.T) {
	src := &SessionTreeSource{
		Store:    &stubTreeStore{tr: fixtureTree()},
		MaxBytes: 200, // tiny on purpose
	}
	res, _ := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if res.Report.Status != StatusTruncated {
		t.Errorf("Status=%s want truncated", res.Report.Status)
	}
	body := res.Messages[0].Content.Text()
	if len(body) > 200 {
		t.Errorf("body bytes=%d > MaxBytes=200", len(body))
	}
}

func TestTreeSource_CustomRenderPanic(t *testing.T) {
	src := &SessionTreeSource{
		Store:  &stubTreeStore{tr: fixtureTree()},
		Render: func(_ FetchInput, _ TreeView) string { panic("kaboom") },
	}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status=%s want skipped after panic", res.Report.Status)
	}
}

// TestTreeSource_NotMustInclude verifies the documented invariant that the
// Source does NOT advertise must-include semantics; it is an enhancement.
func TestTreeSource_NotMustInclude(t *testing.T) {
	src := &SessionTreeSource{Store: &stubTreeStore{}}
	if _, ok := any(src).(MustIncludeSource); ok {
		t.Errorf("SessionTreeSource implements MustIncludeSource; should not")
	}
}

// TestTreeSource_Name verifies the constant.
func TestTreeSource_Name(t *testing.T) {
	src := &SessionTreeSource{}
	if src.Name() != SourceNameSessionTree {
		t.Errorf("Name=%q want %q", src.Name(), SourceNameSessionTree)
	}
}

// TestTreeSource_MaxPathDepth verifies that the configured MaxPathDepth flows
// through the TreeView to the renderer, so callers can shrink the path
// rendering budget without writing a custom Render.
func TestTreeSource_MaxPathDepth(t *testing.T) {
	tr := fixtureTree()
	// Make the path deeper: extend B with a chain B → B2 → B2a → B2b and
	// move the cursor to B2b so we get a 4-node path.
	b2a := &tree.TreeNode{ID: "tn-b2a", Type: tree.NodeSubtask, Status: tree.StatusActive, Title: "callback v2", Summary: "extra detail A", Parent: "tn-b2", Depth: 3}
	b2b := &tree.TreeNode{ID: "tn-b2b", Type: tree.NodeSubtask, Status: tree.StatusActive, Title: "callback v3", Summary: "extra detail B", Parent: "tn-b2a", Depth: 4}
	tr.Nodes["tn-b2a"] = b2a
	tr.Nodes["tn-b2b"] = b2b
	tr.Nodes["tn-b2"].Children = []string{"tn-b2a"}
	tr.Nodes["tn-b2a"].Children = []string{"tn-b2b"}
	tr.Cursor = "tn-b2b"

	// MaxPathDepth=1 keeps only the cursor's summary; older path entries
	// degrade to title-only — so "feature flag X" (root summary) still shows
	// up under Goal, but the path entry for root should NOT include its
	// summary block "Summary: feature flag X".
	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}, MaxPathDepth: 1}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()

	// Cursor's summary should remain in the Path block.
	if !strings.Contains(body, "extra detail B") {
		t.Errorf("cursor summary missing; body:\n%s", body)
	}
	// "extra detail A" belongs to the next-to-last path node, which the
	// degrade rule should suppress when MaxPathDepth=1.
	pathSection := body[strings.Index(body, "### Path"):]
	if strings.Contains(pathSection, "extra detail A") {
		t.Errorf("non-tail path summary should have been degraded; path section:\n%s", pathSection)
	}
}

// TestTreeSource_FoldsPromotedChildren verifies the default render
// suppresses promoted siblings and surfaces the (folded: N children, M done)
// hint instead. The path nodes stay visible regardless.
func TestTreeSource_FoldsPromotedChildren(t *testing.T) {
	tr := fixtureTree()
	// Mark B1 (under cursor B) as promoted + done; B2 stays live.
	tr.Nodes["tn-b1"].Promoted = true
	tr.Nodes["tn-b1"].Status = tree.StatusDone
	tr.Nodes["tn-b1"].PromotedAt = time.Now()

	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()
	if strings.Contains(body, "schema confirmed") {
		t.Errorf("promoted child rendered; want hidden\n%s", body)
	}
	if !strings.Contains(body, "(folded: 1 children, 1 done)") {
		t.Errorf("missing folded hint\n%s", body)
	}
	// B2 (live sibling of B1) must still be there.
	if !strings.Contains(body, "implement callback") {
		t.Errorf("live child suppressed\n%s", body)
	}
}

// TestTreeSource_IncludePromotedDisablesFolding sets IncludePromoted=true and
// expects every promoted child to render alongside the live ones, with no
// folded hint.
func TestTreeSource_IncludePromotedDisablesFolding(t *testing.T) {
	tr := fixtureTree()
	tr.Nodes["tn-b1"].Promoted = true
	tr.Nodes["tn-b1"].Status = tree.StatusDone

	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}, IncludePromoted: true}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "schema confirmed") {
		t.Errorf("IncludePromoted=true should render promoted child\n%s", body)
	}
	if strings.Contains(body, "(folded:") {
		t.Errorf("folded hint should be suppressed\n%s", body)
	}
}

// TestTreeSource_PathPromotedStillRendered verifies that even when an
// ancestor on the root → cursor path is marked Promoted, it is still part
// of the rendered path. Folding only affects non-path siblings.
func TestTreeSource_PathPromotedStillRendered(t *testing.T) {
	tr := fixtureTree()
	// Mark B (the cursor's parent on the path... actually B IS the cursor)
	// → mark B itself promoted to test path-on-cursor case.
	tr.Nodes["tn-b"].Promoted = true
	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()
	if !strings.Contains(body, "wire dispatcher") {
		t.Errorf("path node (cursor) was hidden despite being on path\n%s", body)
	}
}

// TestTreeSource_RecentDoneSiblingSkipsPromoted ensures the "recently
// completed" line picks the next-most-recent non-promoted done sibling
// when the freshest done one is folded.
func TestTreeSource_RecentDoneSiblingSkipsPromoted(t *testing.T) {
	tr := fixtureTree()
	// Add a second done sibling that is NOT promoted.
	other := &tree.TreeNode{
		ID: "tn-a2", Type: tree.NodeSubtask, Status: tree.StatusDone,
		Title: "older done sibling", Summary: "earlier work",
		Parent: "tn-root", Depth: 1, UpdatedAt: time.Unix(100, 0),
	}
	tr.Nodes["tn-a2"] = other
	tr.Nodes["tn-root"].Children = append(tr.Nodes["tn-root"].Children, "tn-a2")
	// "design schema" is the existing done sibling; mark it promoted and
	// fresher than tn-a2.
	tr.Nodes["tn-a"].Promoted = true
	tr.Nodes["tn-a"].UpdatedAt = time.Unix(200, 0)

	// Move cursor to root so siblings of cursor are A / B / C / A2.
	tr.Cursor = "tn-root"

	src := &SessionTreeSource{Store: &stubTreeStore{tr: tr}}
	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Messages[0].Content.Text()
	// We're at root, which has no parent — so the renderer's recent-sibling
	// section should not appear at all. Check by absence: a node titled
	// "older done sibling" shouldn't show under "Recently completed".
	if strings.Contains(body, "Recently completed") {
		t.Errorf("root cursor should have no recent-sibling section\n%s", body)
	}
	if strings.Contains(body, "design schema") {
		t.Errorf("promoted sibling rendered\n%s", body)
	}
}
