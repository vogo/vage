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

import "sync"

// PromotionDecider answers whether a parent node's children warrant a
// PromoteNode call. It runs against the parent + the slice of currently-
// eligible children (i.e., already filtered to non-Promoted, non-Pinned).
// Implementations MUST be pure / non-blocking: they run inline on the
// store's write path immediately after a successful AddNode / UpdateNode.
type PromotionDecider interface {
	ShouldPromote(parent *TreeNode, eligibleChildren []*TreeNode) bool
}

// DeciderFunc adapts a plain function to PromotionDecider.
type DeciderFunc func(parent *TreeNode, eligibleChildren []*TreeNode) bool

// ShouldPromote implements PromotionDecider.
func (f DeciderFunc) ShouldPromote(p *TreeNode, c []*TreeNode) bool { return f(p, c) }

// ChildrenCountDecider fires when the parent has at least Min eligible
// children. Min == 0 falls back to DefaultPromotionMinChildren.
type ChildrenCountDecider struct {
	Min int
}

// ShouldPromote implements PromotionDecider.
func (d ChildrenCountDecider) ShouldPromote(_ *TreeNode, eligible []*TreeNode) bool {
	threshold := d.Min
	if threshold <= 0 {
		threshold = DefaultPromotionMinChildren
	}
	return len(eligible) >= threshold
}

// AllChildrenDoneDecider fires when there is at least one eligible child
// and every eligible child has Status == StatusDone. It is meant for the
// "this branch is wrapped up, please fold it" trigger.
type AllChildrenDoneDecider struct{}

// ShouldPromote implements PromotionDecider.
func (AllChildrenDoneDecider) ShouldPromote(_ *TreeNode, eligible []*TreeNode) bool {
	if len(eligible) == 0 {
		return false
	}
	for _, c := range eligible {
		if c.Status != StatusDone {
			return false
		}
	}
	return true
}

// SubtreeBytesDecider fires when the cumulative Title+Summary bytes of the
// eligible children exceed Min. Min == 0 falls back to
// DefaultPromotionMinSubtreeBytes.
type SubtreeBytesDecider struct {
	Min int
}

// ShouldPromote implements PromotionDecider.
func (d SubtreeBytesDecider) ShouldPromote(_ *TreeNode, eligible []*TreeNode) bool {
	threshold := d.Min
	if threshold <= 0 {
		threshold = DefaultPromotionMinSubtreeBytes
	}
	total := 0
	for _, c := range eligible {
		total += len(c.Title) + len(c.Summary)
		if total >= threshold {
			return true
		}
	}
	return false
}

// AnyOf returns a decider that fires when any of the supplied deciders
// fires. With zero deciders it never fires (matches the empty-OR identity).
func AnyOf(deciders ...PromotionDecider) PromotionDecider {
	return DeciderFunc(func(parent *TreeNode, eligible []*TreeNode) bool {
		for _, d := range deciders {
			if d == nil {
				continue
			}
			if d.ShouldPromote(parent, eligible) {
				return true
			}
		}
		return false
	})
}

// AllOf returns a decider that fires when every supplied decider fires.
// With zero deciders it always fires (matches the empty-AND identity);
// callers who want "never" should pass an empty AnyOf instead.
func AllOf(deciders ...PromotionDecider) PromotionDecider {
	return DeciderFunc(func(parent *TreeNode, eligible []*TreeNode) bool {
		for _, d := range deciders {
			if d == nil {
				continue
			}
			if !d.ShouldPromote(parent, eligible) {
				return false
			}
		}
		return true
	})
}

// promotionInflight tracks (sessionID, parentID) pairs that are already
// being promoted. New triggers for the same key are dropped (NOT queued)
// — this is "skip" semantics, not the "merge" semantics of singleflight.
//
// Concurrency: lock-free reads via sync.Map; the per-key bool is written
// under LoadOrStore CAS. Callers MUST clear the entry when done — see
// release.
type promotionInflight struct {
	keys sync.Map // map[string]struct{}
}

// reserve attempts to claim ownership of the given (session, parent) key.
// It returns true on success — the caller is now responsible for calling
// release once the work is done — and false when another goroutine is
// already running, in which case the caller must NOT execute.
func (p *promotionInflight) reserve(sessionID, parentID string) bool {
	key := promotionKey(sessionID, parentID)
	_, loaded := p.keys.LoadOrStore(key, struct{}{})
	return !loaded
}

// release frees the in-flight slot. Idempotent: the entry is deleted even
// if the caller never reserved (no-op).
func (p *promotionInflight) release(sessionID, parentID string) {
	p.keys.Delete(promotionKey(sessionID, parentID))
}

// promotionKey builds the sync.Map key. Inlined so the same string format
// is used for reserve / release / debug logs.
func promotionKey(sessionID, parentID string) string {
	return sessionID + "|" + parentID
}

// eligibleChildren returns the subset of nodes where Promoted=false and
// Pinned=false, preserving order. The slice references the same pointers
// as the input — callers that mutate must clone first. Used by the store
// before invoking the Decider and again before invoking the Promoter.
func eligibleChildren(tr *SessionTree, parent *TreeNode) []*TreeNode {
	if tr == nil || parent == nil {
		return nil
	}
	out := make([]*TreeNode, 0, len(parent.Children))
	for _, cid := range parent.Children {
		c, ok := tr.Nodes[cid]
		if !ok {
			continue
		}
		if c.Promoted || c.Pinned {
			continue
		}
		out = append(out, c)
	}
	return out
}

// defaultAsyncRunner runs fn in its own goroutine. Stores accept a custom
// runner so tests can run promotion synchronously.
func defaultAsyncRunner(fn func()) {
	go fn()
}
