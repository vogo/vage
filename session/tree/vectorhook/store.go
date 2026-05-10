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

	"github.com/vogo/vage/session/tree"
)

// CreateTree delegates to the inner store and indexes the root node
// when creation succeeds. The root summary is often the strongest
// "what is this session about" signal, so dual-indexing it lets
// session_tree-aware sources score "did I see a similar root before".
func (s *Store) CreateTree(ctx context.Context, sessionID string, root tree.TreeNode) (*tree.TreeNode, error) {
	out, err := s.inner.CreateTree(ctx, sessionID, root)
	if err != nil {
		return out, err
	}
	s.indexNodeMaybeAsync(ctx, sessionID, out)
	return out, nil
}

// GetTree is a pure pass-through.
func (s *Store) GetTree(ctx context.Context, sessionID string) (*tree.SessionTree, error) {
	return s.inner.GetTree(ctx, sessionID)
}

// AddNode delegates and indexes the new node when creation succeeds.
func (s *Store) AddNode(ctx context.Context, sessionID, parentID string, n tree.TreeNode) (*tree.TreeNode, error) {
	out, err := s.inner.AddNode(ctx, sessionID, parentID, n)
	if err != nil {
		return out, err
	}
	s.indexNodeMaybeAsync(ctx, sessionID, out)
	return out, nil
}

// UpdateNode delegates and re-indexes — the summary is the primary
// mutable surface and may have been replaced; any prior vector for
// this node is upserted with the new embedding.
func (s *Store) UpdateNode(ctx context.Context, sessionID string, n tree.TreeNode) (*tree.TreeNode, error) {
	out, err := s.inner.UpdateNode(ctx, sessionID, n)
	if err != nil {
		return out, err
	}
	s.indexNodeMaybeAsync(ctx, sessionID, out)
	return out, nil
}

// DeleteNode delegates and (synchronously) removes the matching vector
// document so a subsequent Search cannot surface a freshly-deleted
// node. The MVP only allows leaf deletion, so there is no cascade.
func (s *Store) DeleteNode(ctx context.Context, sessionID, nodeID string) error {
	if err := s.inner.DeleteNode(ctx, sessionID, nodeID); err != nil {
		return err
	}
	s.removeNodeIndex(ctx, sessionID, nodeID)
	return nil
}

// SetCursor is a pure pass-through. The cursor is structural metadata
// (where the LLM is "looking"), not content; no vector update is
// warranted.
func (s *Store) SetCursor(ctx context.Context, sessionID, nodeID string) error {
	return s.inner.SetCursor(ctx, sessionID, nodeID)
}

// DeleteTree delegates and (synchronously) removes every vector
// document tagged with this session id. Best-effort: backends without
// List support log a warning but do not error.
func (s *Store) DeleteTree(ctx context.Context, sessionID string) error {
	if err := s.inner.DeleteTree(ctx, sessionID); err != nil {
		return err
	}
	s.removeAllNodesForSession(ctx, sessionID)
	return nil
}

// PromoteNode delegates and re-indexes the parent when promotion runs
// successfully — the parent's Summary has been replaced with the
// folded children's roll-up. Folded children retain their vectors
// (callers that want to exclude promoted nodes can filter on the
// `promoted` metadata flag, but we deliberately do NOT delete them so
// zoom-in style retrieval stays possible).
func (s *Store) PromoteNode(ctx context.Context, sessionID, nodeID string) (*tree.TreeNode, error) {
	out, err := s.inner.PromoteNode(ctx, sessionID, nodeID)
	if err != nil {
		return out, err
	}
	s.indexNodeMaybeAsync(ctx, sessionID, out)
	return out, nil
}

// GetTreeView is a pure pass-through.
func (s *Store) GetTreeView(ctx context.Context, sessionID string, opts tree.ViewOptions) (*tree.SessionTree, error) {
	return s.inner.GetTreeView(ctx, sessionID, opts)
}
