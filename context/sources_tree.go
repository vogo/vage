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
	"fmt"
	"log/slog"
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
)

// Default sizing knobs for SessionTreeSource. They are conservative on the
// side of "less is more" — the goal is to give the LLM enough structure to
// orient itself without spending more than ~2k tokens of context on tree
// rendering.
const (
	defaultTreeMaxPathDepth     = 6
	defaultTreeMaxSiblingTitles = 8
	defaultTreeMaxBytes         = 8 * 1024
)

// treeTruncSuffix marks an output that has been byte-truncated to fit the
// MaxBytes / Budget cap.
const treeTruncSuffix = "\n... [truncated]\n"

// TreeRenderer formats a fully-resolved TreeView into a single message body.
// Returning "" makes SessionTreeSource emit Status="skipped".
type TreeRenderer func(in FetchInput, view TreeView) string

// TreeView is the resolved view passed to TreeRenderer. The Source pre-walks
// the tree so renderers do not have to repeat the path/cursor/sibling logic.
type TreeView struct {
	Tree              *tree.SessionTree
	Path              []*tree.TreeNode // root → cursor; len >= 1 when populated
	CursorChildren    []*tree.TreeNode // direct children of cursor (truncated to MaxSiblingTitles)
	CursorChildrenN   int              // total non-promoted before truncation
	RecentDoneSibling *tree.TreeNode   // most-recent (non-promoted) done sibling of cursor; may be nil

	// CursorChildrenPromotedN is the number of promoted children skipped
	// from CursorChildren (renderer surfaces them as a "(folded: N children, M done)"
	// hint). Zero when nothing is folded or IncludePromoted=true.
	CursorChildrenPromotedN int

	// CursorChildrenPromotedDone is the number of promoted children whose
	// Status == StatusDone. Always <= CursorChildrenPromotedN.
	CursorChildrenPromotedDone int

	// IncludePromoted carries the source's flag to the renderer: when true,
	// promoted children are mixed into CursorChildren and the folded hint is
	// suppressed.
	IncludePromoted bool

	// MaxPathDepth carries the source's configured path-depth cap to the
	// renderer. 0 means the renderer should fall back to its own default.
	MaxPathDepth int
}

// SessionTreeSource projects the per-session tree into a single system-role
// message. It does not implement MustIncludeSource — the tree is an
// enhancement layer; an absent / failed read must not block the rest of
// the build.
//
// Failure modes are fail-open per Builder convention: nil store, missing
// tree, malformed tree.json, oversized payload — all surface as Status
// "skipped" / "error" / "truncated" with the Builder continuing onto the
// next source.
type SessionTreeSource struct {
	// Store is the per-session backend used for reads. nil disables.
	Store tree.SessionTreeStore

	// MaxPathDepth caps how many path nodes are rendered with summary.
	// Excess nodes near the root degrade to title-only. 0 -> default.
	MaxPathDepth int

	// MaxSiblingTitles caps the cursor's child list to N entries; the
	// remainder shows up as "(... and K more pending)". 0 -> default.
	MaxSiblingTitles int

	// MaxBytes caps the rendered system message size. 0 -> default
	// (8 KiB). Tail-truncation is used when the rendered output exceeds
	// the cap because the head of the output (Goal + path) is the most
	// important context for the next turn.
	MaxBytes int

	// IncludePromoted disables the folding behaviour: when true, nodes
	// with Promoted=true are rendered alongside the live ones and the
	// "(folded: ...)" hint is suppressed. Default false matches the
	// "compress old work" prompt-shaping intent. Use true for zoom-in
	// flows that want full visibility (audit, debugging, manual review).
	//
	// Path nodes (root → cursor) are always rendered regardless of this
	// flag — the navigation spine must survive. Folding only affects
	// non-path siblings/children.
	IncludePromoted bool

	// Render overrides the default renderer.
	Render TreeRenderer
}

// Compile-time conformance.
var _ Source = (*SessionTreeSource)(nil)

// Name returns SourceNameSessionTree.
func (s *SessionTreeSource) Name() string { return SourceNameSessionTree }

// Fetch reads the tree, resolves the path / cursor view, renders it, and
// applies byte / budget caps. All error paths fail-open.
func (s *SessionTreeSource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameSessionTree}

	if s.Store == nil || in.SessionID == "" {
		rep.Status = StatusSkipped
		rep.Note = "no store / no session"
		return FetchResult{Report: rep}, nil
	}

	tr, err := s.Store.GetTree(ctx, in.SessionID)
	if err != nil {
		if errors.Is(err, tree.ErrTreeMissing) {
			rep.Status = StatusSkipped
			rep.Note = "tree missing"
			return FetchResult{Report: rep}, nil
		}
		slog.Warn("vctx: session tree get", "session_id", in.SessionID, "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	view, ok := s.buildView(tr)
	if !ok {
		// A tree with no usable root (corruption or empty Nodes) is
		// fail-open: return skipped, never error, so the rest of the
		// build proceeds.
		rep.Status = StatusError
		rep.Error = "tree has no usable root"
		return FetchResult{Report: rep}, nil
	}

	render := s.Render
	if render == nil {
		render = defaultTreeRender
	}
	render = recoveringTreeRenderer(render)

	text := render(in, view)
	if text == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty render"
		return FetchResult{Report: rep}, nil
	}

	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultTreeMaxBytes
	}
	originalBytes := len(text)
	truncated := false
	if originalBytes > maxBytes {
		text = clampTreeText(text, maxBytes)
		truncated = true
	}

	rep.OriginalCount = originalBytes
	rep.OutputN = 1
	if truncated {
		rep.Status = StatusTruncated
		rep.DroppedN = 1
		rep.Note = fmt.Sprintf("tail-truncated: %d -> %d bytes", originalBytes, len(text))
	} else {
		rep.Status = StatusOK
	}

	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}
	return FetchResult{Messages: []aimodel.Message{msg}, Report: rep}, nil
}

// buildView walks the tree once, gathering everything the renderer needs.
// Returns ok=false when the tree is unusable (no root or root id absent
// from Nodes), in which case the Source emits an error report.
func (s *SessionTreeSource) buildView(tr *tree.SessionTree) (TreeView, bool) {
	if tr == nil || tr.RootID == "" {
		return TreeView{}, false
	}
	root, ok := tr.Nodes[tr.RootID]
	if !ok {
		return TreeView{}, false
	}

	view := TreeView{Tree: tr, MaxPathDepth: s.MaxPathDepth, IncludePromoted: s.IncludePromoted}

	// Path: root → cursor. When the cursor is absent or invalid we walk
	// the tree along the active branch where possible; otherwise the path
	// is just [root].
	cursorID := tr.Cursor
	if cursorID == "" {
		cursorID = tr.RootID
	}
	cursor, ok := tr.Nodes[cursorID]
	if !ok {
		cursor = root
	}
	view.Path = pathFromRoot(tr, root, cursor)

	// Cursor's direct children: the LLM's "what else is on my plate".
	maxSiblings := s.MaxSiblingTitles
	if maxSiblings <= 0 {
		maxSiblings = defaultTreeMaxSiblingTitles
	}
	for _, cid := range cursor.Children {
		c, ok := tr.Nodes[cid]
		if !ok {
			continue
		}
		if c.Promoted && !s.IncludePromoted {
			view.CursorChildrenPromotedN++
			if c.Status == tree.StatusDone {
				view.CursorChildrenPromotedDone++
			}
			continue
		}
		view.CursorChildrenN++
		if len(view.CursorChildren) < maxSiblings {
			view.CursorChildren = append(view.CursorChildren, c)
		}
	}

	// Recently-completed sibling: the parent's most-recently-updated done
	// child that is not the cursor. It signals "what just shipped" so the
	// next turn picks up where the previous one finished.
	if cursor.Parent != "" {
		if parent, ok := tr.Nodes[cursor.Parent]; ok {
			view.RecentDoneSibling = mostRecentDoneSibling(tr, parent, cursor.ID, s.IncludePromoted)
		}
	}

	return view, true
}

// pathFromRoot walks from root following Children until the cursor is
// reached. When the cursor is not a descendant (corruption), the function
// falls back to the [root, cursor] sequence — the renderer still has
// something usable. The returned slice is owned by the caller.
func pathFromRoot(tr *tree.SessionTree, root, cursor *tree.TreeNode) []*tree.TreeNode {
	if root == cursor {
		return []*tree.TreeNode{root}
	}
	// BFS to find the cursor; we record parents so we can rebuild the path.
	parents := map[string]string{root.ID: ""}
	queue := []string{root.ID}
	found := false
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node := tr.Nodes[id]
		if node == nil {
			continue
		}
		if node.ID == cursor.ID {
			found = true
			break
		}
		for _, cid := range node.Children {
			if _, seen := parents[cid]; seen {
				continue
			}
			parents[cid] = id
			queue = append(queue, cid)
		}
	}
	if !found {
		return []*tree.TreeNode{root, cursor}
	}
	// Walk back from cursor to root via parents, then reverse.
	rev := []*tree.TreeNode{cursor}
	for id := parents[cursor.ID]; id != ""; id = parents[id] {
		if n, ok := tr.Nodes[id]; ok {
			rev = append(rev, n)
		} else {
			break
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// mostRecentDoneSibling returns the parent's most-recently-updated done
// child whose id != skipID (the cursor itself). When includePromoted is
// false the search skips promoted siblings — the renderer's intent there
// is "show what's still live", not "remind the LLM of folded subtrees".
// Returns nil when no such sibling exists.
func mostRecentDoneSibling(tr *tree.SessionTree, parent *tree.TreeNode, skipID string, includePromoted bool) *tree.TreeNode {
	var best *tree.TreeNode
	for _, cid := range parent.Children {
		if cid == skipID {
			continue
		}
		c, ok := tr.Nodes[cid]
		if !ok {
			continue
		}
		if c.Status != tree.StatusDone {
			continue
		}
		if c.Promoted && !includePromoted {
			continue
		}
		if best == nil || c.UpdatedAt.After(best.UpdatedAt) {
			best = c
		}
	}
	return best
}

// recoveringTreeRenderer wraps a renderer so a panicking caller-supplied
// implementation does not bring down the Builder. Recovered panics map to
// an empty render (Source emits "skipped"), consistent with the fail-open
// contract.
func recoveringTreeRenderer(r TreeRenderer) TreeRenderer {
	return func(in FetchInput, view TreeView) (out string) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Warn("vctx: tree renderer panicked", "panic", rec)
				out = ""
			}
		}()
		return r(in, view)
	}
}

// defaultTreeRender prints the goal, root→cursor path, cursor's children,
// and the most-recently-completed sibling. The layout is deliberately
// readable as Markdown so it appears as a navigation outline to the LLM
// rather than a chunk of structured data.
func defaultTreeRender(_ FetchInput, view TreeView) string {
	if view.Tree == nil || len(view.Path) == 0 {
		return ""
	}
	maxPathDepth := defaultTreeMaxPathDepth
	if view.MaxPathDepth > 0 {
		maxPathDepth = view.MaxPathDepth
	}

	var b strings.Builder
	b.WriteString("## Session Tree\n")
	b.WriteString("(Persistent task structure. Use this as a navigation aid: where are we, how does this fit the overall goal.)\n\n")

	root := view.Path[0]
	b.WriteString("### Goal\n")
	writeNodeLine(&b, root, "")
	if root.Summary != "" {
		fmt.Fprintf(&b, "  Summary: %s\n", root.Summary)
	}
	b.WriteByte('\n')

	if len(view.Path) > 1 {
		b.WriteString("### Path (root → cursor)\n")
		degradeBefore := 0
		if len(view.Path) > maxPathDepth {
			degradeBefore = len(view.Path) - maxPathDepth
		}
		for i, n := range view.Path {
			marker := ""
			if i == len(view.Path)-1 {
				marker = " ← cursor"
			}
			fmt.Fprintf(&b, "%d. ", i+1)
			writeNodeLine(&b, n, marker)
			if i >= degradeBefore && n.Summary != "" {
				fmt.Fprintf(&b, "   Summary: %s\n", n.Summary)
			}
		}
		b.WriteByte('\n')
	}

	cursor := view.Path[len(view.Path)-1]
	if len(view.CursorChildren) > 0 || view.CursorChildrenN > 0 || view.CursorChildrenPromotedN > 0 {
		b.WriteString("### Cursor's children\n")
		for _, c := range view.CursorChildren {
			writeNodeLine(&b, c, "")
		}
		if view.CursorChildrenN > len(view.CursorChildren) {
			fmt.Fprintf(&b, "(... and %d more)\n", view.CursorChildrenN-len(view.CursorChildren))
		}
		if !view.IncludePromoted && view.CursorChildrenPromotedN > 0 {
			fmt.Fprintf(&b, "(folded: %d children, %d done)\n",
				view.CursorChildrenPromotedN, view.CursorChildrenPromotedDone)
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "### Cursor's children\n(none — cursor %s is a leaf)\n\n", cursor.ID)
	}

	if view.RecentDoneSibling != nil {
		b.WriteString("### Recently completed (sibling)\n")
		writeNodeLine(&b, view.RecentDoneSibling, "")
		if view.RecentDoneSibling.Summary != "" {
			fmt.Fprintf(&b, "  Summary: %s\n", view.RecentDoneSibling.Summary)
		}
		b.WriteByte('\n')
	}

	b.WriteString("(Status legend: pending / active / done / blocked / superseded)\n")
	return b.String()
}

// writeNodeLine renders one node row: "[type] [status] title<marker>".
func writeNodeLine(b *strings.Builder, n *tree.TreeNode, marker string) {
	fmt.Fprintf(b, "- [%s] [%s] %s%s\n", n.Type, n.Status, n.Title, marker)
}

// clampTreeText truncates s tail-first so the head of the rendering (Goal
// + path) survives. A short marker is appended so the LLM does not treat
// the partial text as authoritative.
func clampTreeText(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	if maxBytes <= len(treeTruncSuffix) {
		return s[:maxBytes]
	}
	return s[:maxBytes-len(treeTruncSuffix)] + treeTruncSuffix
}
