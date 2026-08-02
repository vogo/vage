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

package vectorhook

import (
	"context"
	"testing"

	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/vector"
)

// newTestStore wraps a fresh in-memory tree + map vector store +
// HashEmbedder. Synchronous mode keeps tests deterministic — async is
// covered by a dedicated test that calls Wait().
func newTestStore(t *testing.T, async bool) (*Store, *vector.MapVectorStore) {
	t.Helper()
	inner := tree.NewMapTreeStore()
	vstore := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(64)

	opts := []Option{}
	if !async {
		opts = append(opts, WithSynchronous())
	}
	s, err := WrapStore(inner, vstore, emb, opts...)
	if err != nil {
		t.Fatalf("WrapStore: %v", err)
	}
	return s, vstore
}

func TestWrapStore_NilInner(t *testing.T) {
	if _, err := WrapStore(nil, vector.NewMapVectorStore(), vector.NewHashEmbedder(8)); err == nil {
		t.Errorf("expected error on nil inner")
	}
}

func TestWrapStore_NilVectorOrEmbedder_PassThrough(t *testing.T) {
	// Wiring should tolerate a partial config: setup may compute
	// "vector index disabled" upstream and still wrap to centralise
	// the tree handle.
	inner := tree.NewMapTreeStore()
	s, err := WrapStore(inner, nil, nil, WithSynchronous())
	if err != nil {
		t.Fatalf("WrapStore: %v", err)
	}

	if _, err := s.CreateTree(context.Background(), "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "S",
	}); err != nil {
		t.Fatalf("CreateTree: %v", err)
	}
	// Tree was written; the absence of vector wiring must not error.
}

func TestCreateTree_IndexesRoot(t *testing.T) {
	s, vstore := newTestStore(t, false)
	root, err := s.CreateTree(context.Background(), "sess-1", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "build a thing",
	})
	if err != nil {
		t.Fatalf("CreateTree: %v", err)
	}

	docs, err := vstore.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("len = %d, want 1", len(docs))
	}
	want := DocumentID("sess-1", root.ID)
	if docs[0].ID != want {
		t.Errorf("ID = %q, want %q", docs[0].ID, want)
	}
	// Metadata fields are populated.
	for _, k := range []string{MetadataKeySessionID, MetadataKeyNodeID, MetadataKeyDepth, MetadataKeyStatus, MetadataKeyType} {
		if _, ok := docs[0].Metadata[k]; !ok {
			t.Errorf("metadata missing key %q: %+v", k, docs[0].Metadata)
		}
	}
}

func TestAddNode_IndexesChild(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess-1", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "S",
	})
	child, err := s.AddNode(ctx, "sess-1", root.ID, tree.TreeNode{
		Type: tree.NodeSubtask, Title: "step", Summary: "child summary",
	})
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	docs, _ := vstore.List(ctx)
	if len(docs) != 2 {
		t.Errorf("len = %d, want 2", len(docs))
	}
	// Find the child doc.
	wantID := DocumentID("sess-1", child.ID)
	var found bool
	for _, d := range docs {
		if d.ID == wantID {
			found = true
			if d.Text != "child summary" {
				t.Errorf("Text = %q", d.Text)
			}
		}
	}
	if !found {
		t.Errorf("child doc %q not found", wantID)
	}
}

func TestUpdateNode_UpsertsVector(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "before",
	})

	updated := *root
	updated.Summary = "after"
	if _, err := s.UpdateNode(ctx, "sess", updated); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	docs, _ := vstore.List(ctx)
	if len(docs) != 1 {
		t.Fatalf("len = %d, want 1", len(docs))
	}
	if docs[0].Text != "after" {
		t.Errorf("Text = %q, want 'after'", docs[0].Text)
	}
}

func TestUpdateNode_EmptySummaryDeletes(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "S",
	})

	updated := *root
	updated.Summary = ""
	if _, err := s.UpdateNode(ctx, "sess", updated); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	docs, _ := vstore.List(ctx)
	if len(docs) != 0 {
		t.Errorf("len = %d, want 0", len(docs))
	}
}

func TestDeleteNode_RemovesVector(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "S",
	})
	child, _ := s.AddNode(ctx, "sess", root.ID, tree.TreeNode{
		Type: tree.NodeSubtask, Title: "leaf", Summary: "leaf-summary",
	})
	if err := s.DeleteNode(ctx, "sess", child.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	docs, _ := vstore.List(ctx)
	if len(docs) != 1 {
		t.Errorf("len = %d, want 1 (root only)", len(docs))
	}
	for _, d := range docs {
		if d.ID == DocumentID("sess", child.ID) {
			t.Errorf("child vector lingered: %+v", d)
		}
	}
}

func TestDeleteTree_RemovesAllNodesForSession(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess-A", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "rootA",
	})
	_, _ = s.AddNode(ctx, "sess-A", root.ID, tree.TreeNode{
		Type: tree.NodeSubtask, Title: "x", Summary: "x",
	})
	// A second session that must NOT be touched.
	_, _ = s.CreateTree(ctx, "sess-B", tree.TreeNode{
		Type: tree.NodeGoal, Title: "rootB", Summary: "rootB",
	})

	if err := s.DeleteTree(ctx, "sess-A"); err != nil {
		t.Fatalf("DeleteTree: %v", err)
	}
	docs, _ := vstore.List(ctx)
	if len(docs) != 1 {
		t.Errorf("len = %d, want 1 (sess-B remains)", len(docs))
	}
	for _, d := range docs {
		if got, _ := d.Metadata[MetadataKeySessionID].(string); got != "sess-B" {
			t.Errorf("unexpected residual: %+v", d)
		}
	}
}

func TestPromoteNode_ReindexesParent(t *testing.T) {
	// Build a parent with enough children to fire the default
	// PromotionDecider, then call PromoteNode and verify the parent's
	// stored Text is the promoted summary.
	inner := tree.NewMapTreeStore(
		tree.WithMapPromoter(constantPromoter{summary: "ROLLED-UP"}),
	)
	vstore := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(64)
	s, err := WrapStore(inner, vstore, emb, WithSynchronous())
	if err != nil {
		t.Fatalf("WrapStore: %v", err)
	}
	ctx := context.Background()

	root, _ := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "rootS",
	})
	for i := range 3 {
		_, _ = s.AddNode(ctx, "sess", root.ID, tree.TreeNode{
			Type:    tree.NodeSubtask,
			Title:   "child",
			Summary: "child summary " + string(rune('0'+i)),
		})
	}

	if _, err := s.PromoteNode(ctx, "sess", root.ID); err != nil {
		t.Fatalf("PromoteNode: %v", err)
	}

	docs, _ := vstore.List(ctx)
	for _, d := range docs {
		if d.ID == DocumentID("sess", root.ID) && d.Text != "ROLLED-UP" {
			t.Errorf("root Text = %q, want ROLLED-UP", d.Text)
		}
	}
}

func TestSetCursor_NoVectorWrite(t *testing.T) {
	s, vstore := newTestStore(t, false)
	ctx := context.Background()
	root, _ := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "S",
	})

	before, _ := vstore.List(ctx)
	if err := s.SetCursor(ctx, "sess", root.ID); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	after, _ := vstore.List(ctx)
	if len(after) != len(before) {
		t.Errorf("SetCursor mutated vector store: %d → %d", len(before), len(after))
	}
}

func TestAsync_WaitFlushesPending(t *testing.T) {
	s, vstore := newTestStore(t, true) // async
	ctx := context.Background()
	if _, err := s.CreateTree(ctx, "sess", tree.TreeNode{
		Type: tree.NodeGoal, Title: "root", Summary: "async test",
	}); err != nil {
		t.Fatalf("CreateTree: %v", err)
	}
	s.Wait()
	docs, _ := vstore.List(ctx)
	if len(docs) != 1 {
		t.Errorf("after Wait len = %d, want 1", len(docs))
	}
}

func TestDocumentID_Stable(t *testing.T) {
	got := DocumentID("sid", "tn-abc")
	if got != "tree:sid:tn-abc" {
		t.Errorf("got = %q", got)
	}
}

// constantPromoter returns a fixed summary regardless of children;
// makes promotion tests deterministic without spinning up an LLM.
type constantPromoter struct{ summary string }

func (c constantPromoter) Summarize(_ context.Context, _ *tree.TreeNode, _ []*tree.TreeNode) (string, error) {
	return c.summary, nil
}
