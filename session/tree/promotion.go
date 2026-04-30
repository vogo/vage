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

import "time"

// summarySourceMetaKey is the Metadata key Stores stamp on parent +
// folded children when PromoteNode runs. Values: "user" (caller-written,
// implicit default when the key is absent) or "promotion".
const summarySourceMetaKey = "summary_source"

// SummarySourceUser / SummarySourcePromotion are the canonical values for
// summarySourceMetaKey. Agents may inspect them to distinguish caller-
// written summaries from machine-aggregated ones.
const (
	SummarySourceUser      = "user"
	SummarySourcePromotion = "promotion"

	// SummarySourceMetaKey is the public alias for the metadata key
	// stamped by the store. Exported so callers can inspect TreeNode
	// metadata without re-typing the literal.
	SummarySourceMetaKey = summarySourceMetaKey
)

// cloneEligible deep-copies the slice and each element so the caller can
// mutate the snapshot without racing with concurrent store writes.
func cloneEligible(in []*TreeNode) []*TreeNode {
	if len(in) == 0 {
		return nil
	}
	out := make([]*TreeNode, len(in))
	for i, n := range in {
		out[i] = cloneNode(n)
	}
	return out
}

// applyPromotion flips Promoted=true on every child in eligibleSnap that is
// still attached to the parent and still eligible (not Pinned, not already
// Promoted) at commit time, and — only when at least one child actually
// folded — writes newSummary back to parent. It returns the actual folded
// count, which can be lower than len(eligibleSnap) when concurrent
// UpdateNode calls have changed children between the snapshot and the
// commit. When the count is zero the parent is left untouched (no Summary
// rewrite, no Metadata stamp, no UpdatedAt bump) so a race that drains the
// eligible set is indistinguishable from the no-op short-circuit.
//
// Caller MUST hold the store's per-tree write lock.
func applyPromotion(parent *TreeNode, tr *SessionTree, newSummary string, eligibleSnap []*TreeNode, now time.Time) int {
	snapIndex := make(map[string]struct{}, len(eligibleSnap))
	for _, c := range eligibleSnap {
		snapIndex[c.ID] = struct{}{}
	}

	folded := 0
	for _, cid := range parent.Children {
		if _, planned := snapIndex[cid]; !planned {
			continue
		}
		c, ok := tr.Nodes[cid]
		if !ok || c.Pinned || c.Promoted {
			continue
		}
		c.Promoted = true
		c.PromotedAt = now
		c.UpdatedAt = now
		setMetadata(c, summarySourceMetaKey, SummarySourcePromotion)
		folded++
	}

	if folded == 0 {
		return 0
	}
	parent.Summary = newSummary
	parent.UpdatedAt = now
	setMetadata(parent, summarySourceMetaKey, SummarySourcePromotion)
	return folded
}

// setMetadata writes a single key into n.Metadata, allocating the map
// lazily. Used by applyPromotion and any future store-side annotators.
func setMetadata(n *TreeNode, key string, value any) {
	if n.Metadata == nil {
		n.Metadata = make(map[string]any, 1)
	}
	n.Metadata[key] = value
}

// filterPromotedFromTree mutates tr in place to drop every node where
// Promoted=true and every node reachable only through such a node. The
// remaining parent.Children lists are rewritten to omit the removed ids.
// Returns tr for fluent chaining.
//
// The walk is purely structural and cursor-agnostic; renderers that need
// to preserve path-on-cursor nodes should use GetTree (full) and apply
// their own filter.
func filterPromotedFromTree(tr *SessionTree) *SessionTree {
	if tr == nil || len(tr.Nodes) == 0 {
		return tr
	}

	drop := make(map[string]struct{}, len(tr.Nodes))
	for id, n := range tr.Nodes {
		if n.Promoted {
			drop[id] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return tr
	}

	// Add descendants of dropped nodes — even non-promoted children of a
	// promoted parent are not visible because the parent is gone.
	for id := range drop {
		collectDescendants(tr, id, drop)
	}

	for id := range drop {
		delete(tr.Nodes, id)
	}
	for _, n := range tr.Nodes {
		if len(n.Children) == 0 {
			continue
		}
		filtered := n.Children[:0]
		for _, cid := range n.Children {
			if _, removed := drop[cid]; removed {
				continue
			}
			filtered = append(filtered, cid)
		}
		// Reslice into a fresh array so the underlying backing storage
		// is not shared with the caller's pre-clone copy.
		n.Children = append([]string(nil), filtered...)
	}
	return tr
}

// collectDescendants walks the children of id and adds every reached node
// to drop. Used by filterPromotedFromTree to ensure subtrees rooted at a
// promoted node disappear entirely from the view.
func collectDescendants(tr *SessionTree, id string, drop map[string]struct{}) {
	parent, ok := tr.Nodes[id]
	if !ok {
		return
	}
	for _, cid := range parent.Children {
		if _, seen := drop[cid]; seen {
			continue
		}
		drop[cid] = struct{}{}
		collectDescendants(tr, cid, drop)
	}
}
