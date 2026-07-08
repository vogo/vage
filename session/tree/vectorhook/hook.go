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

// Package vectorhook glues vage/session/tree.SessionTreeStore to a
// vector.VectorStore so that node summaries are dual-indexed: the tree
// keeps the goal/subtask structure, the vector store handles
// "similarity-tuned recall across siblings". §4.8.3 of the design doc
// calls this the "structure + similarity" pair, and §4.8.6 step 3 was
// the missing wiring this package provides.
//
// The hook is a thin DECORATOR over any SessionTreeStore — Map, File,
// or future SQL backends compose identically. Reads are pure
// pass-through; writes are pass-through plus a non-blocking sidecar
// that:
//
//   - on AddNode / UpdateNode / PromoteNode → embeds the node's Summary
//     and upserts a Document keyed `tree:<sid>:<nid>`;
//   - on DeleteNode / DeleteTree → removes the corresponding Document
//     (or, for DeleteTree, every Document tagged with that session id).
//
// Failure mode is "fail-open" matching vector/archivehook: an embed or
// store error logs a warning and never propagates back to the tree
// caller. The tree is the source of truth; the vector copy is a
// performance-side index that can lag or be rebuilt.
package vectorhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/vector"
)

// DocumentIDPrefix is the leading segment on every vector document
// produced by this package. Exporting it lets callers list / filter
// "tree-sourced" documents without parsing the body.
const DocumentIDPrefix = "tree:"

// MetadataKey* are the standardised metadata keys this package writes
// into vector.Document.Metadata. Exported so consumers (predicate
// helpers, dashboards) can rely on stable strings.
const (
	MetadataKeySessionID = "session_id"
	MetadataKeyNodeID    = "node_id"
	MetadataKeyDepth     = "depth"
	MetadataKeyStatus    = "status"
	MetadataKeyType      = "type"
	MetadataKeyPromoted  = "promoted"
)

// DocumentID composes the canonical document id for (sessionID, nodeID).
// The format is intentionally human-readable so log lines and HTTP
// dumps stay debuggable; the prefix is `DocumentIDPrefix`.
func DocumentID(sessionID, nodeID string) string {
	return DocumentIDPrefix + sessionID + ":" + nodeID
}

// Option configures a wrapped store.
type Option func(*config)

type config struct {
	async  bool
	logger *slog.Logger
}

// WithSynchronous makes Add/Update/Promote-side writes block until the
// embed + vector.Add returns. Default is asynchronous (fire-and-forget
// goroutine), which matches the design doc's "tree write must not be
// gated on a network round-trip".
//
// Synchronous mode is intended for tests where the caller wants to
// observe vector state immediately after a tree mutation without
// racing the goroutine.
func WithSynchronous() Option {
	return func(c *config) { c.async = false }
}

// WithLogger sets the slog.Logger used for warnings on embed/store
// failures. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// Store is a SessionTreeStore decorator that mirrors writes into a
// vector.VectorStore.
type Store struct {
	inner    tree.SessionTreeStore
	vstore   vector.VectorStore
	embedder vector.Embedder

	cfg config

	// pending tracks in-flight async writes so callers can Wait()
	// before checking vector contents. Useful for tests; production
	// callers do not need to wait.
	pending sync.WaitGroup
}

// Compile-time conformance. The decorator must satisfy the same
// interface as the wrapped store.
var _ tree.SessionTreeStore = (*Store)(nil)

// WrapStore returns inner wrapped with vector indexing. nil
// vstore/embedder are tolerated: the wrapper logs a one-shot warning
// and degrades to a pass-through, so callers can wire the decorator
// unconditionally and let the host config flag toggle activeness.
//
// Returns an error only when inner is nil — the rest is fail-open.
func WrapStore(inner tree.SessionTreeStore, vstore vector.VectorStore, embedder vector.Embedder, opts ...Option) (*Store, error) {
	if inner == nil {
		return nil, errors.New("vectorhook: inner store is nil")
	}
	cfg := config{async: true, logger: slog.Default()}
	for _, o := range opts {
		o(&cfg)
	}
	return &Store{
		inner:    inner,
		vstore:   vstore,
		embedder: embedder,
		cfg:      cfg,
	}, nil
}

// Wait blocks until every in-flight async write has completed. Tests
// only — do not call from production code paths.
func (s *Store) Wait() {
	s.pending.Wait()
}

// indexNode upserts a single node's Summary into the vector store.
// Empty Summary (no body to embed) is treated as a delete so that an
// UpdateNode that clears Summary does not leave a stale entry behind.
//
// The fail-open contract: embed or Add errors are logged at Warn but
// never returned.
func (s *Store) indexNode(ctx context.Context, sessionID string, n *tree.TreeNode) {
	if s.vstore == nil || s.embedder == nil || n == nil {
		return
	}
	docID := DocumentID(sessionID, n.ID)

	// A node with no Summary is not informative for similarity
	// search; mirror that by removing any prior entry. The vector
	// store may silently succeed on missing ids (MapVectorStore
	// behaviour), so we don't gate on errors.
	if strings.TrimSpace(n.Summary) == "" {
		if err := s.vstore.Delete(ctx, docID); err != nil && !errors.Is(err, vector.ErrNotFound) {
			s.warn("vectorhook: delete on empty summary", err, sessionID, n.ID)
		}
		return
	}

	vec, err := s.embedder.Embed(ctx, n.Summary)
	if err != nil {
		s.warn("vectorhook: embed", err, sessionID, n.ID)
		return
	}

	doc := vector.Document{
		ID:        docID,
		Text:      n.Summary,
		Embedding: vec,
		Metadata:  metadataFor(sessionID, n),
		CreatedAt: n.UpdatedAt,
	}
	if err := s.vstore.Add(ctx, doc); err != nil {
		s.warn("vectorhook: add", err, sessionID, n.ID)
	}
}

// indexNodeMaybeAsync runs indexNode on a goroutine when configured
// async, otherwise synchronously. The goroutine path uses the parent
// ctx for cancellation; the parent's Done channel propagates so a
// caller cancelling the tree write also cancels the embed.
//
// Concurrency: pending tracks the goroutine so tests can Wait().
func (s *Store) indexNodeMaybeAsync(ctx context.Context, sessionID string, n *tree.TreeNode) {
	if !s.cfg.async {
		s.indexNode(ctx, sessionID, n)
		return
	}
	s.pending.Go(func() {
		s.indexNode(ctx, sessionID, n)
	})
}

// removeNodeIndex deletes the document matching (sessionID, nodeID).
// Synchronous: callers (DeleteNode) want the vector to be gone before
// they return so a subsequent search does not surface a freshly-deleted
// node. Errors are still fail-open (logged, not returned).
func (s *Store) removeNodeIndex(ctx context.Context, sessionID, nodeID string) {
	if s.vstore == nil {
		return
	}
	if err := s.vstore.Delete(ctx, DocumentID(sessionID, nodeID)); err != nil && !errors.Is(err, vector.ErrNotFound) {
		s.warn("vectorhook: remove node", err, sessionID, nodeID)
	}
}

// removeAllNodesForSession is best-effort: lists everything (when the
// store supports List) and deletes ids that match the session prefix.
// Backends that return ErrNotSupported on List degrade to "no-op" —
// the deleted tree leaves orphan vectors until a List-capable backend
// or external sweeper cleans them up.
func (s *Store) removeAllNodesForSession(ctx context.Context, sessionID string) {
	if s.vstore == nil {
		return
	}
	docs, err := s.vstore.List(ctx)
	if err != nil {
		if errors.Is(err, vector.ErrNotSupported) {
			s.warn("vectorhook: tree delete skipped (List unsupported)", err, sessionID, "")
			return
		}
		s.warn("vectorhook: tree delete list", err, sessionID, "")
		return
	}
	prefix := DocumentIDPrefix + sessionID + ":"
	for _, d := range docs {
		if !strings.HasPrefix(d.ID, prefix) {
			continue
		}
		if err := s.vstore.Delete(ctx, d.ID); err != nil && !errors.Is(err, vector.ErrNotFound) {
			s.warn("vectorhook: tree delete", err, sessionID, d.ID)
		}
	}
}

// metadataFor builds the canonical metadata bag for a node document.
// Keys are stable strings (exported as MetadataKey*) so predicate
// helpers downstream can rely on them.
func metadataFor(sessionID string, n *tree.TreeNode) map[string]any {
	return map[string]any{
		MetadataKeySessionID: sessionID,
		MetadataKeyNodeID:    n.ID,
		MetadataKeyDepth:     n.Depth,
		MetadataKeyStatus:    string(n.Status),
		MetadataKeyType:      string(n.Type),
		MetadataKeyPromoted:  n.Promoted,
	}
}

// warn logs a fail-open warning. Detail is uniform across call sites
// so log greps stay simple.
func (s *Store) warn(msg string, err error, sessionID, nodeID string) {
	if s.cfg.logger == nil {
		return
	}
	s.cfg.logger.Warn(msg,
		slog.String("session_id", sessionID),
		slog.String("node_id", nodeID),
		slog.String("err", fmt.Sprintf("%v", err)),
	)
}
