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
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

// MapTreeStore is the in-memory SessionTreeStore. A single sync.RWMutex
// guards every tree owned by the store; per-tree locking is unnecessary
// because tree operations are infrequent compared with event/state writes.
type MapTreeStore struct {
	mu    sync.RWMutex
	trees map[string]*SessionTree

	hooks *hook.Manager
	now   func() time.Time
}

// MapOption configures a MapTreeStore.
type MapOption func(*MapTreeStore)

// WithMapHookManager wires the store to a hook.Manager. Mutations dispatch
// EventSessionTreeUpdated after the in-memory state has been updated. nil
// is allowed and disables dispatch.
func WithMapHookManager(m *hook.Manager) MapOption {
	return func(s *MapTreeStore) { s.hooks = m }
}

// WithMapClock injects a clock function. Tests use it to make timestamps
// deterministic; production callers leave it at nil for time.Now.
func WithMapClock(fn func() time.Time) MapOption {
	return func(s *MapTreeStore) {
		if fn != nil {
			s.now = fn
		}
	}
}

// Compile-time interface conformance.
var _ SessionTreeStore = (*MapTreeStore)(nil)

// NewMapTreeStore constructs an empty in-memory tree store.
func NewMapTreeStore(opts ...MapOption) *MapTreeStore {
	s := &MapTreeStore{
		trees: make(map[string]*SessionTree),
		now:   time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateTree initialises a tree for sessionID with root as its goal node.
func (s *MapTreeStore) CreateTree(ctx context.Context, sessionID string, root TreeNode) (*TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if root.Type == "" {
		root.Type = NodeGoal
	}
	if root.Status == "" {
		root.Status = StatusActive
	}
	if err := validateNodePayload(root); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if _, exists := s.trees[sessionID]; exists {
		s.mu.Unlock()
		return nil, ErrAlreadyExists
	}

	now := s.now()
	rootCopy := cloneNode(&root)
	rootCopy.ID = generateNodeID(now)
	rootCopy.Parent = ""
	rootCopy.Children = nil
	rootCopy.Depth = 0
	rootCopy.CreatedAt = now
	rootCopy.UpdatedAt = now

	tr := &SessionTree{
		SessionID: sessionID,
		RootID:    rootCopy.ID,
		Cursor:    rootCopy.ID,
		Nodes:     map[string]*TreeNode{rootCopy.ID: rootCopy},
		UpdatedAt: now,
	}
	s.trees[sessionID] = tr
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpCreate, rootCopy, 1)
	return cloneNode(rootCopy), nil
}

// GetTree returns a deep copy of the named tree.
func (s *MapTreeStore) GetTree(ctx context.Context, sessionID string) (*SessionTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	tr, ok := s.trees[sessionID]
	if !ok {
		return nil, ErrTreeMissing
	}
	return cloneTree(tr), nil
}

// AddNode appends a child under parentID.
func (s *MapTreeStore) AddNode(ctx context.Context, sessionID, parentID string, n TreeNode) (*TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := validateNodeID(parentID); err != nil {
		return nil, err
	}
	if n.Status == "" {
		n.Status = StatusPending
	}
	if err := validateNodePayload(n); err != nil {
		return nil, err
	}

	s.mu.Lock()
	tr, ok := s.trees[sessionID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrTreeMissing
	}
	parent, ok := tr.Nodes[parentID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: parent %q", ErrNotFound, parentID)
	}
	if len(tr.Nodes) >= MaxNodes {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %d nodes already present", ErrTreeFull, len(tr.Nodes))
	}

	now := s.now()
	child := cloneNode(&n)
	child.ID = generateNodeID(now)
	child.Parent = parent.ID
	child.Children = nil
	child.Depth = parent.Depth + 1
	child.CreatedAt = now
	child.UpdatedAt = now

	tr.Nodes[child.ID] = child
	parent.Children = append(parent.Children, child.ID)
	parent.UpdatedAt = now
	tr.UpdatedAt = now

	count := len(tr.Nodes)
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpAdd, child, count)
	return cloneNode(child), nil
}

// UpdateNode rewrites the mutable subset of an existing node.
func (s *MapTreeStore) UpdateNode(ctx context.Context, sessionID string, n TreeNode) (*TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := validateNodeID(n.ID); err != nil {
		return nil, err
	}
	if err := validateNodeUpdate(n); err != nil {
		return nil, err
	}

	s.mu.Lock()
	tr, ok := s.trees[sessionID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrTreeMissing
	}
	cur, ok := tr.Nodes[n.ID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrNotFound, n.ID)
	}
	if n.Type != "" && n.Type != cur.Type {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: type %q -> %q", ErrImmutableField, cur.Type, n.Type)
	}
	if n.Parent != "" && n.Parent != cur.Parent {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: parent %q -> %q", ErrImmutableField, cur.Parent, n.Parent)
	}

	now := s.now()
	applyUpdate(cur, &n, now)
	tr.UpdatedAt = now
	updated := cloneNode(cur)
	count := len(tr.Nodes)
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpUpdate, updated, count)
	return updated, nil
}

// DeleteNode removes a leaf node.
func (s *MapTreeStore) DeleteNode(ctx context.Context, sessionID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateNodeID(nodeID); err != nil {
		return err
	}

	s.mu.Lock()
	tr, ok := s.trees[sessionID]
	if !ok {
		s.mu.Unlock()
		return ErrTreeMissing
	}
	target, ok := tr.Nodes[nodeID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotFound, nodeID)
	}
	if nodeID == tr.RootID {
		s.mu.Unlock()
		return fmt.Errorf("%w: cannot delete root; use DeleteTree", ErrInvalidArgument)
	}
	if len(target.Children) > 0 {
		s.mu.Unlock()
		return fmt.Errorf("%w: %d children", ErrHasChildren, len(target.Children))
	}

	now := s.now()
	parent := tr.Nodes[target.Parent]
	if parent != nil {
		parent.Children = removeID(parent.Children, nodeID)
		parent.UpdatedAt = now
	}
	delete(tr.Nodes, nodeID)
	if tr.Cursor == nodeID {
		tr.Cursor = tr.RootID
	}
	tr.UpdatedAt = now

	snapshot := cloneNode(target)
	count := len(tr.Nodes)
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpDelete, snapshot, count)
	return nil
}

// SetCursor moves the cursor to nodeID, or clears it when nodeID == "".
func (s *MapTreeStore) SetCursor(ctx context.Context, sessionID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if nodeID != "" {
		if err := validateNodeID(nodeID); err != nil {
			return err
		}
	}

	s.mu.Lock()
	tr, ok := s.trees[sessionID]
	if !ok {
		s.mu.Unlock()
		return ErrTreeMissing
	}
	var cursorNode *TreeNode
	if nodeID != "" {
		cursorNode, ok = tr.Nodes[nodeID]
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("%w: %q", ErrNotFound, nodeID)
		}
	}
	now := s.now()
	tr.Cursor = nodeID
	tr.UpdatedAt = now
	count := len(tr.Nodes)
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpCursor, cursorNode, count)
	return nil
}

// DeleteTree removes the entire tree.
func (s *MapTreeStore) DeleteTree(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	if _, ok := s.trees[sessionID]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.trees, sessionID)
	s.mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpDeleteTree, nil, 0)
	return nil
}

// dispatch publishes a mutation event when a hook manager is configured.
// nil-safe; nil node turns into empty NodeID/NodeType/Status fields.
func (s *MapTreeStore) dispatch(ctx context.Context, sessionID, op string, n *TreeNode, count int) {
	if s.hooks == nil {
		return
	}
	data := schema.SessionTreeUpdatedData{
		SessionID: sessionID,
		Operation: op,
		NodeCount: count,
	}
	if n != nil {
		data.NodeID = n.ID
		data.NodeType = string(n.Type)
		data.Status = string(n.Status)
	}
	s.hooks.Dispatch(ctx, schema.NewEvent(schema.EventSessionTreeUpdated, "", sessionID, data))
}

// applyUpdate copies the mutable fields from src into dst in place,
// refreshing the UpdatedAt timestamp. Type and Parent are not touched
// (they are immutable). Slice / map fields are reallocated so the caller's
// post-call mutation of src does not leak into the store.
func applyUpdate(dst *TreeNode, src *TreeNode, now time.Time) {
	dst.Title = src.Title
	dst.Summary = src.Summary
	if src.Status != "" {
		dst.Status = src.Status
	}
	dst.ContentRef = src.ContentRef
	dst.EmbeddingID = src.EmbeddingID
	if src.Evidence != nil {
		dst.Evidence = append([]string(nil), src.Evidence...)
	} else {
		dst.Evidence = nil
	}
	if src.Supersedes != nil {
		dst.Supersedes = append([]string(nil), src.Supersedes...)
	} else {
		dst.Supersedes = nil
	}
	dst.Pinned = src.Pinned
	if src.Metadata != nil {
		dst.Metadata = make(map[string]any, len(src.Metadata))
		maps.Copy(dst.Metadata, src.Metadata)
	} else {
		dst.Metadata = nil
	}
	dst.UpdatedAt = now
}

// removeID returns ids with the first occurrence of v removed. Order is
// preserved; if v is absent ids is returned unchanged.
func removeID(ids []string, v string) []string {
	for i, id := range ids {
		if id == v {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}
