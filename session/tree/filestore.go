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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600

	// On-disk layout under <root>/<sessionID>/tree/.
	treeDirName  = "tree"
	treeFileName = "tree.json"
)

// FileTreeStore persists session trees as a single JSON file per session.
// On-disk layout:
//
//	<root>/<sessionID>/tree/tree.json
//
// The root is shared with vage/session.FileSessionStore and
// vage/workspace.FileWorkspace by convention so that
// SessionStore.Delete(sessionID) wipes the metadata, events, state KV,
// workspace, and the tree in one os.RemoveAll. Callers therefore do not
// need to coordinate cleanup.
//
// Concurrency: writes against the same session are serialised by a per-
// session sync.Mutex (allocated lazily). Reads are intentionally lock-free
// — atomic-renamed file writes mean a concurrent reader either sees the
// previous version or the new one. Cross-process coordination is not
// provided; running multiple writers against the same root is undefined.
//
// Hook dispatch happens AFTER the per-session lock is released. This
// matches MapTreeStore and prevents a sync hook that calls back into the
// store from deadlocking on its own session's mutex.
type FileTreeStore struct {
	root  string
	locks sync.Map // map[sessionID]*sync.Mutex

	hooks *hook.Manager
	now   func() time.Time

	promoter        Promoter
	promotionDecide PromotionDecider
	asyncRunner     func(func())
	inflight        promotionInflight
}

// FileOption configures a FileTreeStore.
type FileOption func(*FileTreeStore)

// WithFileHookManager wires the store to a hook.Manager. nil disables.
func WithFileHookManager(m *hook.Manager) FileOption {
	return func(s *FileTreeStore) { s.hooks = m }
}

// WithFileClock injects a clock function for deterministic tests.
func WithFileClock(fn func() time.Time) FileOption {
	return func(s *FileTreeStore) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithFilePromoter configures the Promoter used by PromoteNode and the
// auto-trigger pipeline. nil disables both.
func WithFilePromoter(p Promoter) FileOption {
	return func(s *FileTreeStore) { s.promoter = p }
}

// WithFilePromotionDecider configures the trigger that fires automatic
// promotion after AddNode / UpdateNode. nil disables auto-promotion;
// PromoteNode remains usable as a synchronous primitive.
func WithFilePromotionDecider(d PromotionDecider) FileOption {
	return func(s *FileTreeStore) { s.promotionDecide = d }
}

// WithFilePromotionAsync injects the runner used to execute auto-triggered
// promotions. The default is `go fn()`. Tests inject a synchronous runner
// to avoid timing flakes.
func WithFilePromotionAsync(fn func(func())) FileOption {
	return func(s *FileTreeStore) {
		if fn != nil {
			s.asyncRunner = fn
		}
	}
}

// Compile-time conformance.
var _ SessionTreeStore = (*FileTreeStore)(nil)

// NewFileTreeStore constructs a FileTreeStore rooted at the given directory.
// The directory is created (with parents) if missing; an empty root returns
// ErrInvalidArgument.
func NewFileTreeStore(root string, opts ...FileOption) (*FileTreeStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root directory is empty", ErrInvalidArgument)
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("tree: create root %q: %w", root, err)
	}
	s := &FileTreeStore{
		root:        root,
		now:         time.Now,
		asyncRunner: defaultAsyncRunner,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Root returns the configured root directory; primarily for tests.
func (s *FileTreeStore) Root() string { return s.root }

// PathOf returns the on-disk tree directory for sessionID. Returns "" when
// sessionID fails validation, so callers can rely on a non-empty result
// implying a usable path.
func (s *FileTreeStore) PathOf(sessionID string) string {
	if validateSessionID(sessionID) != nil {
		return ""
	}
	return filepath.Join(s.root, sessionID, treeDirName)
}

func (s *FileTreeStore) lockFor(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *FileTreeStore) sessionDir(id string) string {
	return filepath.Join(s.root, id, treeDirName)
}

func (s *FileTreeStore) treePath(id string) string {
	return filepath.Join(s.sessionDir(id), treeFileName)
}

// CreateTree writes the initial tree file.
func (s *FileTreeStore) CreateTree(ctx context.Context, sessionID string, root TreeNode) (*TreeNode, error) {
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

	mu := s.lockFor(sessionID)
	mu.Lock()

	if _, err := os.Stat(s.treePath(sessionID)); err == nil {
		mu.Unlock()
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		mu.Unlock()
		return nil, fmt.Errorf("tree: stat: %w", err)
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
	if err := s.writeTree(sessionID, tr); err != nil {
		mu.Unlock()
		return nil, err
	}
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpCreate, rootCopy, 1)
	return cloneNode(rootCopy), nil
}

// GetTree reads tree.json and returns the deserialised tree.
func (s *FileTreeStore) GetTree(ctx context.Context, sessionID string) (*SessionTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	return s.readTree(sessionID)
}

// AddNode writes a new child under parentID.
func (s *FileTreeStore) AddNode(ctx context.Context, sessionID, parentID string, n TreeNode) (*TreeNode, error) {
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

	mu := s.lockFor(sessionID)
	mu.Lock()

	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	parent, ok := tr.Nodes[parentID]
	if !ok {
		mu.Unlock()
		return nil, fmt.Errorf("%w: parent %q", ErrNotFound, parentID)
	}
	if len(tr.Nodes) >= MaxNodes {
		mu.Unlock()
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

	if err := s.writeTree(sessionID, tr); err != nil {
		mu.Unlock()
		return nil, err
	}
	count := len(tr.Nodes)
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpAdd, child, count)
	s.maybeTriggerPromotion(sessionID, parentID)
	return cloneNode(child), nil
}

// UpdateNode rewrites the mutable subset of an existing node.
func (s *FileTreeStore) UpdateNode(ctx context.Context, sessionID string, n TreeNode) (*TreeNode, error) {
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

	mu := s.lockFor(sessionID)
	mu.Lock()

	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	cur, ok := tr.Nodes[n.ID]
	if !ok {
		mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrNotFound, n.ID)
	}
	if n.Type != "" && n.Type != cur.Type {
		mu.Unlock()
		return nil, fmt.Errorf("%w: type %q -> %q", ErrImmutableField, cur.Type, n.Type)
	}
	if n.Parent != "" && n.Parent != cur.Parent {
		mu.Unlock()
		return nil, fmt.Errorf("%w: parent %q -> %q", ErrImmutableField, cur.Parent, n.Parent)
	}

	now := s.now()
	applyUpdate(cur, &n, now)
	tr.UpdatedAt = now

	if err := s.writeTree(sessionID, tr); err != nil {
		mu.Unlock()
		return nil, err
	}

	updated := cloneNode(cur)
	parentID := cur.Parent
	count := len(tr.Nodes)
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpUpdate, updated, count)
	if parentID != "" {
		s.maybeTriggerPromotion(sessionID, parentID)
	}
	return updated, nil
}

// DeleteNode removes a leaf node.
func (s *FileTreeStore) DeleteNode(ctx context.Context, sessionID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateNodeID(nodeID); err != nil {
		return err
	}

	mu := s.lockFor(sessionID)
	mu.Lock()

	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return err
	}
	target, ok := tr.Nodes[nodeID]
	if !ok {
		mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotFound, nodeID)
	}
	if nodeID == tr.RootID {
		mu.Unlock()
		return fmt.Errorf("%w: cannot delete root; use DeleteTree", ErrInvalidArgument)
	}
	if len(target.Children) > 0 {
		mu.Unlock()
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
	if err := s.writeTree(sessionID, tr); err != nil {
		mu.Unlock()
		return err
	}
	count := len(tr.Nodes)
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpDelete, snapshot, count)
	return nil
}

// SetCursor moves the cursor.
func (s *FileTreeStore) SetCursor(ctx context.Context, sessionID, nodeID string) error {
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

	mu := s.lockFor(sessionID)
	mu.Lock()

	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return err
	}
	var cursorNode *TreeNode
	if nodeID != "" {
		n, ok := tr.Nodes[nodeID]
		if !ok {
			mu.Unlock()
			return fmt.Errorf("%w: %q", ErrNotFound, nodeID)
		}
		cursorNode = n
	}
	now := s.now()
	tr.Cursor = nodeID
	tr.UpdatedAt = now
	if err := s.writeTree(sessionID, tr); err != nil {
		mu.Unlock()
		return err
	}
	// Snapshot the dispatch payload before releasing the lock so the
	// readers do not race with concurrent writers on the in-store node
	// pointer.
	var snapshot *TreeNode
	if cursorNode != nil {
		snapshot = cloneNode(cursorNode)
	}
	count := len(tr.Nodes)
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpCursor, snapshot, count)
	return nil
}

// DeleteTree removes the per-session tree directory.
func (s *FileTreeStore) DeleteTree(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		// Invalid id means it could not have been created — idempotent.
		if errors.Is(err, ErrInvalidArgument) {
			return nil
		}
		return err
	}

	mu := s.lockFor(sessionID)
	mu.Lock()

	if err := os.RemoveAll(s.sessionDir(sessionID)); err != nil {
		mu.Unlock()
		return fmt.Errorf("tree: delete: %w", err)
	}
	s.locks.Delete(sessionID)
	mu.Unlock()

	s.dispatch(ctx, sessionID, schema.SessionTreeOpDeleteTree, nil, 0)
	return nil
}

// readTree loads tree.json. ErrTreeMissing is returned for "no file" so
// the rest of the API can rely on a typed sentinel.
func (s *FileTreeStore) readTree(sessionID string) (*SessionTree, error) {
	data, err := os.ReadFile(s.treePath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrTreeMissing
		}
		return nil, fmt.Errorf("tree: read: %w", err)
	}
	var tr SessionTree
	if err := json.Unmarshal(data, &tr); err != nil {
		// Treat malformed JSON as "missing" from the API's viewpoint
		// so the SessionTreeSource's fail-open path kicks in. The raw
		// error is logged at the caller site.
		return nil, fmt.Errorf("tree: unmarshal: %w", err)
	}
	if tr.Nodes == nil {
		tr.Nodes = make(map[string]*TreeNode)
	}
	return &tr, nil
}

// writeTree marshals tr and replaces tree.json atomically.
func (s *FileTreeStore) writeTree(sessionID string, tr *SessionTree) error {
	if err := os.MkdirAll(s.sessionDir(sessionID), dirPerm); err != nil {
		return fmt.Errorf("tree: create dir: %w", err)
	}
	data, err := json.Marshal(tr)
	if err != nil {
		return fmt.Errorf("tree: marshal: %w", err)
	}
	return writeFileAtomic(s.treePath(sessionID), data)
}

// dispatch publishes EventSessionTreeUpdated when a hook manager is configured.
func (s *FileTreeStore) dispatch(ctx context.Context, sessionID, op string, n *TreeNode, count int) {
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

// GetTreeView returns the tree filtered by opts.
func (s *FileTreeStore) GetTreeView(ctx context.Context, sessionID string, opts ViewOptions) (*SessionTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	tr, err := s.readTree(sessionID)
	if err != nil {
		return nil, err
	}
	if opts.IncludePromoted {
		return tr, nil
	}
	return filterPromotedFromTree(tr), nil
}

// PromoteNode aggregates eligible children of nodeID into nodeID's
// summary using the configured Promoter.
func (s *FileTreeStore) PromoteNode(ctx context.Context, sessionID, nodeID string) (*TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := validateNodeID(nodeID); err != nil {
		return nil, err
	}
	if s.promoter == nil {
		return nil, errPromoterNotConfigured
	}

	mu := s.lockFor(sessionID)

	// Phase 1: snapshot eligible children under the per-session lock.
	mu.Lock()
	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	parent, ok := tr.Nodes[nodeID]
	if !ok {
		mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrNotFound, nodeID)
	}
	parentSnap := cloneNode(parent)
	eligibleSnap := cloneEligible(eligibleChildren(tr, parent))
	mu.Unlock()

	if len(eligibleSnap) == 0 {
		return parentSnap, nil
	}

	// Phase 2: invoke the Promoter outside the lock and dispatch Started.
	s.dispatchStarted(ctx, sessionID, nodeID, len(eligibleSnap))
	newSummary, err := s.promoter.Summarize(ctx, parentSnap, eligibleSnap)
	if err != nil {
		s.dispatchFailed(ctx, sessionID, nodeID, err)
		return nil, err
	}
	newSummary = clampSummary(newSummary, SummaryMaxBytes)

	// Phase 3: re-acquire the lock and apply.
	mu.Lock()
	tr, err = s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		s.dispatchFailed(ctx, sessionID, nodeID, err)
		return nil, err
	}
	parent, ok = tr.Nodes[nodeID]
	if !ok {
		mu.Unlock()
		err := fmt.Errorf("%w: %q", ErrNotFound, nodeID)
		s.dispatchFailed(ctx, sessionID, nodeID, err)
		return nil, err
	}
	now := s.now()
	folded := applyPromotion(parent, tr, newSummary, eligibleSnap, now)
	// Skip the disk write when applyPromotion produced no mutation; the
	// snapshot raced empty (every planned child got promoted/pinned in
	// the gap) and writing back a byte-identical tree would be wasted IO.
	if folded > 0 {
		tr.UpdatedAt = now
		if err := s.writeTree(sessionID, tr); err != nil {
			mu.Unlock()
			s.dispatchFailed(ctx, sessionID, nodeID, err)
			return nil, err
		}
	}
	updatedParent := cloneNode(parent)
	mu.Unlock()

	// Completed pairs with Started even when folded==0; see MapTreeStore
	// for the rationale.
	s.dispatchCompleted(ctx, sessionID, nodeID, folded, len(updatedParent.Summary))
	return updatedParent, nil
}

// maybeTriggerPromotion mirrors MapTreeStore.maybeTriggerPromotion. It runs
// the configured decider against a deep-copied snapshot of the parent +
// its eligible children, and dispatches PromoteNode via the async runner
// when the decider fires. The in-flight set drops duplicate triggers for
// the same (session, parent) instead of queuing them.
func (s *FileTreeStore) maybeTriggerPromotion(sessionID, parentID string) {
	if s.promoter == nil || s.promotionDecide == nil {
		return
	}

	mu := s.lockFor(sessionID)
	mu.Lock()
	tr, err := s.readTree(sessionID)
	if err != nil {
		mu.Unlock()
		return
	}
	parent, ok := tr.Nodes[parentID]
	if !ok {
		mu.Unlock()
		return
	}
	parentSnap := cloneNode(parent)
	eligibleSnap := cloneEligible(eligibleChildren(tr, parent))
	mu.Unlock()

	if !s.promotionDecide.ShouldPromote(parentSnap, eligibleSnap) {
		return
	}
	if !s.inflight.reserve(sessionID, parentID) {
		return
	}

	runner := s.asyncRunner
	if runner == nil {
		runner = defaultAsyncRunner
	}
	runner(func() {
		defer s.inflight.release(sessionID, parentID)
		_, _ = s.PromoteNode(context.Background(), sessionID, parentID)
	})
}

// dispatchStarted publishes EventSessionTreePromotionStarted.
func (s *FileTreeStore) dispatchStarted(ctx context.Context, sessionID, parentID string, eligible int) {
	if s.hooks == nil {
		return
	}
	s.hooks.Dispatch(ctx, schema.NewEvent(schema.EventSessionTreePromotionStarted, "", sessionID,
		schema.SessionTreePromotionStartedData{SessionID: sessionID, ParentID: parentID, Eligible: eligible}))
}

// dispatchCompleted publishes EventSessionTreePromotionCompleted.
func (s *FileTreeStore) dispatchCompleted(ctx context.Context, sessionID, parentID string, folded, summaryBytes int) {
	if s.hooks == nil {
		return
	}
	s.hooks.Dispatch(ctx, schema.NewEvent(schema.EventSessionTreePromotionCompleted, "", sessionID,
		schema.SessionTreePromotionCompletedData{
			SessionID: sessionID, ParentID: parentID, FoldedCount: folded, NewSummaryBytes: summaryBytes,
		}))
}

// dispatchFailed publishes EventSessionTreePromotionFailed.
func (s *FileTreeStore) dispatchFailed(ctx context.Context, sessionID, parentID string, err error) {
	if s.hooks == nil || err == nil {
		return
	}
	s.hooks.Dispatch(ctx, schema.NewEvent(schema.EventSessionTreePromotionFailed, "", sessionID,
		schema.SessionTreePromotionFailedData{SessionID: sessionID, ParentID: parentID, Error: err.Error()}))
}

// writeFileAtomic encodes data via temp file + rename so a crashed write
// leaves the previous version intact and a concurrent reader either sees
// the old file or the new one — never a partial write. Mirrors the helper
// in vage/workspace.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("tree: open tmp: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("tree: write: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("tree: fsync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("tree: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("tree: rename: %w", err)
	}
	return nil
}
