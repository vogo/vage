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

import "context"

// ViewOptions configures GetTreeView. Currently it only controls whether
// promoted (folded) nodes are kept in the returned tree; future extensions
// (depth caps, type filters, since-cursor pruning) live here without
// breaking callers that pass the zero value.
type ViewOptions struct {
	// IncludePromoted, when true, returns the full tree exactly like
	// GetTree. The default false drops every node with Promoted=true and
	// every descendant under one — useful for "folded" prompt views and
	// HTTP listings that want to surface only the live working set.
	//
	// Note: GetTreeView does NOT understand cursor semantics. A renderer
	// that needs path-aware folding (root → cursor must always survive)
	// should call GetTree (full tree) and apply its own filter.
	IncludePromoted bool
}

// SessionTreeStore is the persistent backend for one or many SessionTrees.
// All methods are concurrency-safe; writes against the same session are
// serialised inside the implementation.
//
// The interface is deliberately narrow:
//
//   - No "open mutation" — callers receive copies and write back via UpdateNode.
//   - No reparent / move — this MVP treats Type and Parent as immutable; reshape
//     is "create new + mark old superseded".
//   - No bulk export — when persistence is needed, GetTree returns the whole
//     tree and the caller serialises it.
type SessionTreeStore interface {
	// CreateTree initialises an empty tree for sessionID with a root node
	// taking its values (Type/Title/Summary/Metadata/etc.) from root. The
	// Type defaults to NodeGoal when root.Type == "". Returns the
	// materialised root with the generated ID. Returns ErrAlreadyExists if a
	// tree already exists for that session.
	CreateTree(ctx context.Context, sessionID string, root TreeNode) (*TreeNode, error)

	// GetTree returns the entire tree for sessionID, or ErrTreeMissing if
	// none exists. The returned SessionTree is safe to mutate; nothing the
	// caller does affects the store.
	GetTree(ctx context.Context, sessionID string) (*SessionTree, error)

	// AddNode appends a new child under parentID. The store assigns the new
	// node's ID, Parent, Depth, CreatedAt/UpdatedAt; user-supplied values
	// for those fields are ignored. Returns the materialised node.
	AddNode(ctx context.Context, sessionID, parentID string, n TreeNode) (*TreeNode, error)

	// UpdateNode rewrites the mutable subset of an existing node identified
	// by n.ID. Type and Parent are immutable; supplying different values
	// returns ErrImmutableField. Returns the updated node.
	UpdateNode(ctx context.Context, sessionID string, n TreeNode) (*TreeNode, error)

	// DeleteNode removes nodeID. The MVP only allows leaf deletion: removing
	// a node with children returns ErrHasChildren. Removing the root is
	// rejected with ErrInvalidArgument — use DeleteTree instead. Idempotent
	// on already-missing ids only via ErrNotFound (not silent), to match
	// the "node id is meaningful" contract.
	DeleteNode(ctx context.Context, sessionID, nodeID string) error

	// SetCursor moves the cursor to nodeID. nodeID == "" clears the cursor.
	// nodeID must reference an existing node otherwise ErrNotFound.
	SetCursor(ctx context.Context, sessionID, nodeID string) error

	// DeleteTree removes the entire tree. Idempotent: deleting a non-existent
	// session returns nil.
	DeleteTree(ctx context.Context, sessionID string) error

	// PromoteNode aggregates the eligible (non-Pinned, non-Promoted) children
	// of nodeID into nodeID's summary using the store-configured Promoter,
	// then marks each folded child Promoted=true (with PromotedAt=now). The
	// parent's Summary is replaced with the Promoter output (clamped to
	// SummaryMaxBytes), and Metadata["summary_source"] is set to
	// "promotion" on both parent and folded children.
	//
	// When zero children are eligible, the call is a no-op: the parent is
	// returned unchanged and no event is dispatched. When the configured
	// Promoter returns an error, no field on parent or children is mutated.
	//
	// Concurrency: PromoteNode releases the per-session write lock while
	// the Promoter is running (LLM calls may take seconds), then re-acquires
	// it to write the new summary. Children that have changed during the
	// gap are silently skipped from folding; PromoteNode never returns a
	// stale-state error.
	//
	// Errors:
	//   - ErrTreeMissing: no tree for sessionID
	//   - ErrNotFound:    nodeID not present
	//   - ErrInvalidArgument: input validation failure
	//   - whatever Promoter.Summarize returns (passed through)
	PromoteNode(ctx context.Context, sessionID, nodeID string) (*TreeNode, error)

	// GetTreeView returns a deep-copied tree filtered by opts.
	// opts.IncludePromoted=true is equivalent to GetTree.
	GetTreeView(ctx context.Context, sessionID string, opts ViewOptions) (*SessionTree, error)
}
