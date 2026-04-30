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

// Package tree provides a hierarchical Session Tree memory model: per-session
// goal/subtask trees that the ContextBuilder can project onto the LLM prompt
// as a navigation spine. It supports manual node management; automatic
// promotion ("reflection") and dual-index search are out of scope for this
// package.
//
// The package is a sibling of vage/session and vage/workspace: identical
// validation conventions, atomic on-disk writes, per-session locking, shared
// session-id space. It does not import vage/session — they coexist by
// convention rather than by dependency.
package tree

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"
)

// NodeType enumerates the canonical kinds of node a SessionTree can hold.
// goal/subtask form the structural "skeleton"; fact/observation/artifact_ref
// hang off the skeleton as evidence. Adding a new value requires updating
// every store implementation and renderer.
type NodeType string

// NodeType constants.
const (
	NodeGoal        NodeType = "goal"
	NodeSubtask     NodeType = "subtask"
	NodeFact        NodeType = "fact"
	NodeObservation NodeType = "observation"
	NodeArtifactRef NodeType = "artifact_ref"
)

// Valid reports whether t is one of the recognised NodeType constants.
func (t NodeType) Valid() bool {
	switch t {
	case NodeGoal, NodeSubtask, NodeFact, NodeObservation, NodeArtifactRef:
		return true
	default:
		return false
	}
}

// NodeStatus enumerates lifecycle states a node may occupy.
type NodeStatus string

// NodeStatus constants.
const (
	StatusPending    NodeStatus = "pending"
	StatusActive     NodeStatus = "active"
	StatusDone       NodeStatus = "done"
	StatusBlocked    NodeStatus = "blocked"
	StatusSuperseded NodeStatus = "superseded"
)

// Valid reports whether s is one of the recognised NodeStatus constants.
func (s NodeStatus) Valid() bool {
	switch s {
	case StatusPending, StatusActive, StatusDone, StatusBlocked, StatusSuperseded:
		return true
	default:
		return false
	}
}

// Capacity / safety constants.
const (
	// TitleMaxBytes caps a TreeNode.Title size. 200 bytes ≈ 65–80 Chinese
	// characters — large enough for a meaningful headline, small enough to
	// stay cheap when injected into every turn's prompt path.
	TitleMaxBytes = 200

	// SummaryMaxBytes caps a TreeNode.Summary size. 2 KiB is the soft cap on
	// "how much per node can fit in the LLM's working memory" — the ContextSource
	// then trades summaries off against the prompt budget at render time.
	SummaryMaxBytes = 2 * 1024

	// MaxNodes caps the total number of nodes per tree. Crossing this cap
	// returns ErrTreeFull — without automatic promotion, more nodes only
	// translates to a noisier prompt. When promotion lands the cap can lift.
	MaxNodes = 1024

	// NodeIDMaxLen caps the length of an externally-supplied or generated
	// node id.
	NodeIDMaxLen = 128

	// DefaultPromotionMinChildren is the default ChildrenCountDecider
	// threshold: parents with at least this many eligible (non-promoted,
	// non-pinned) children are candidates for folding.
	DefaultPromotionMinChildren = 8

	// DefaultPromotionMinSubtreeBytes is the default SubtreeBytesDecider
	// threshold: if the eligible children's combined Title+Summary bytes
	// exceed this number, the parent is a candidate for folding.
	DefaultPromotionMinSubtreeBytes = 8 * 1024

	// nodeIDPrefix is the required prefix on every node id. It distinguishes
	// tree node ids from session ids on log output and keeps the address
	// space disjoint should the two ever share a key column in the future.
	nodeIDPrefix = "tn-"

	sessionIDMaxLen = 128
)

// nodeIDPattern is the validation regex applied to TreeNode.ID. The body
// (after nodeIDPrefix) shares the character class with session.IDPattern so
// that ids stay file-system safe.
var nodeIDPattern = regexp.MustCompile(`^tn-[A-Za-z0-9._-]{1,128}$`)

// sessionIDPattern duplicates the regex used by vage/session.IDPattern so
// this package does not need to import vage/session. The two stay in sync by
// convention; both are short, stable, and tested.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Sentinel errors returned by SessionTreeStore implementations.
var (
	// ErrInvalidArgument signals input that fails validation: empty
	// session/node id, malformed id, oversized title/summary, missing
	// required fields, etc. Always wrapped with detail.
	ErrInvalidArgument = errors.New("tree: invalid argument")

	// ErrNotFound is returned when an operation references a node that
	// does not exist in the named tree.
	ErrNotFound = errors.New("tree: node not found")

	// ErrTreeMissing is returned when no tree exists for the given session.
	ErrTreeMissing = errors.New("tree: tree does not exist")

	// ErrAlreadyExists is returned by CreateTree when a tree already exists
	// for the session.
	ErrAlreadyExists = errors.New("tree: tree already exists")

	// ErrTreeFull is returned when an Add would push the node count past
	// MaxNodes. The caller is expected to prune or wait for promotion to
	// fold older subtrees before retrying.
	ErrTreeFull = errors.New("tree: node count exceeds limit")

	// ErrHasChildren is returned by DeleteNode when the target still has
	// children — the MVP only supports leaf deletion to avoid silently
	// dropping subtrees.
	ErrHasChildren = errors.New("tree: node has children, cannot delete")

	// ErrImmutableField is returned by UpdateNode when a caller attempts
	// to change a field that is fixed at creation (Type, Parent).
	ErrImmutableField = errors.New("tree: field is immutable")
)

// TreeNode is a single addressable point in the SessionTree. The struct
// shape is part of the public wire format for FileTreeStore's tree.json:
// all field tags use omitempty so adding fields stays backward compatible
// without bumping a schema version.
//
// Mutable fields (free to change via UpdateNode): Title, Summary, Status,
// ContentRef, EmbeddingID, Evidence, Supersedes, Pinned, Metadata. Type and
// Parent are fixed at creation; reshape requires Delete + Add.
type TreeNode struct {
	ID     string     `json:"id"`
	Type   NodeType   `json:"type"`
	Status NodeStatus `json:"status"`

	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`

	ContentRef  string   `json:"content_ref,omitempty"`
	EmbeddingID string   `json:"embedding_id,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Supersedes  []string `json:"supersedes,omitempty"`
	Pinned      bool     `json:"pinned,omitempty"`

	// Promoted marks the node as having been folded into its parent's
	// summary by a PromoteNode call. The node remains in the tree (so
	// audit and zoom-in stay possible) but the default render skips it.
	// Pinned and Promoted are mutually exclusive at the producer side
	// (PromoteNode never folds Pinned children); a manual UpdateNode can
	// in principle set both, in which case Pinned wins on render.
	Promoted bool `json:"promoted,omitempty"`

	// PromotedAt is set when Promoted transitions to true. Zero on
	// freshly added or manually-cleared nodes. Uses `omitzero` because
	// time.Time is a struct and `omitempty` would never elide it.
	PromotedAt time.Time `json:"promoted_at,omitzero"`

	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`

	Depth     int       `json:"depth"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Metadata map[string]any `json:"metadata,omitempty"`
}

// SessionTree holds the full tree for one session: identity, root, the
// optional cursor, and a flat node map keyed by id. The shape is also the
// on-disk format for FileTreeStore's tree.json.
type SessionTree struct {
	SessionID string               `json:"session_id"`
	RootID    string               `json:"root_id"`
	Cursor    string               `json:"cursor,omitempty"`
	Nodes     map[string]*TreeNode `json:"nodes"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// generateNodeID returns a sortable, filesystem-safe node id of the form
// "tn-<unix-nanos>-<8-hex>". On the rare event crypto/rand fails the suffix
// is dropped so the id remains valid (still unique within nanosecond resolution
// for a single process); callers may inspect the second return value when
// they care about randomness loss in a test.
func generateNodeID(now time.Time) string {
	prefix := nodeIDPrefix + strconv.FormatInt(now.UnixNano(), 10)

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return prefix
	}

	return prefix + "-" + hex.EncodeToString(buf[:])
}

// validateSessionID applies the same pattern as vage/session so a tree can
// piggy-back on any sessionID minted by the session package. Duplicating the
// regex avoids a circular dependency.
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}
	if len(id) > sessionIDMaxLen {
		return fmt.Errorf("%w: session id length %d exceeds %d", ErrInvalidArgument, len(id), sessionIDMaxLen)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w: session id %q is reserved", ErrInvalidArgument, id)
	}
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("%w: session id %q does not match pattern", ErrInvalidArgument, id)
	}
	return nil
}

// validateNodeID checks an id against nodeIDPattern.
func validateNodeID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: node id is empty", ErrInvalidArgument)
	}
	if len(id) > NodeIDMaxLen+len(nodeIDPrefix) {
		return fmt.Errorf("%w: node id length %d exceeds %d", ErrInvalidArgument, len(id), NodeIDMaxLen+len(nodeIDPrefix))
	}
	if !nodeIDPattern.MatchString(id) {
		return fmt.Errorf("%w: node id %q does not match pattern", ErrInvalidArgument, id)
	}
	return nil
}

// validateNodePayload checks the user-supplied portion of a TreeNode. It is
// invoked from CreateTree (root) and AddNode (children). UpdateNode reuses
// validateNodeUpdate which only checks the mutable surface.
func validateNodePayload(n TreeNode) error {
	if !n.Type.Valid() {
		return fmt.Errorf("%w: node type %q is unknown", ErrInvalidArgument, n.Type)
	}
	if n.Status != "" && !n.Status.Valid() {
		return fmt.Errorf("%w: node status %q is unknown", ErrInvalidArgument, n.Status)
	}
	if n.Title == "" {
		return fmt.Errorf("%w: title is empty", ErrInvalidArgument)
	}
	if len(n.Title) > TitleMaxBytes {
		return fmt.Errorf("%w: title length %d exceeds %d", ErrInvalidArgument, len(n.Title), TitleMaxBytes)
	}
	if len(n.Summary) > SummaryMaxBytes {
		return fmt.Errorf("%w: summary length %d exceeds %d", ErrInvalidArgument, len(n.Summary), SummaryMaxBytes)
	}
	return nil
}

// validateNodeUpdate checks the mutable payload of an UpdateNode call.
// Title can be empty in updates? No — title is required on every node, so
// reject "" even on update.
func validateNodeUpdate(n TreeNode) error {
	if n.Status != "" && !n.Status.Valid() {
		return fmt.Errorf("%w: node status %q is unknown", ErrInvalidArgument, n.Status)
	}
	if n.Title == "" {
		return fmt.Errorf("%w: title is empty", ErrInvalidArgument)
	}
	if len(n.Title) > TitleMaxBytes {
		return fmt.Errorf("%w: title length %d exceeds %d", ErrInvalidArgument, len(n.Title), TitleMaxBytes)
	}
	if len(n.Summary) > SummaryMaxBytes {
		return fmt.Errorf("%w: summary length %d exceeds %d", ErrInvalidArgument, len(n.Summary), SummaryMaxBytes)
	}
	return nil
}

// cloneNode returns a copy of n that owns its Children / Evidence /
// Supersedes / Metadata containers, so the caller cannot mutate store-side
// state through the returned pointer. Map values are copied shallowly:
// a deep clone of arbitrary `any` is impractical and the convention is that
// metadata values are immutable.
func cloneNode(n *TreeNode) *TreeNode {
	if n == nil {
		return nil
	}
	out := *n
	if n.Children != nil {
		out.Children = make([]string, len(n.Children))
		copy(out.Children, n.Children)
	}
	if n.Evidence != nil {
		out.Evidence = make([]string, len(n.Evidence))
		copy(out.Evidence, n.Evidence)
	}
	if n.Supersedes != nil {
		out.Supersedes = make([]string, len(n.Supersedes))
		copy(out.Supersedes, n.Supersedes)
	}
	if n.Metadata != nil {
		out.Metadata = make(map[string]any, len(n.Metadata))
		maps.Copy(out.Metadata, n.Metadata)
	}
	return &out
}

// cloneTree returns a deep-enough copy of t that shares no mutable state
// with the store: the Nodes map and every node it points at are independent
// copies. Used by Get on every read.
func cloneTree(t *SessionTree) *SessionTree {
	if t == nil {
		return nil
	}
	out := *t
	if t.Nodes != nil {
		out.Nodes = make(map[string]*TreeNode, len(t.Nodes))
		for k, v := range t.Nodes {
			out.Nodes[k] = cloneNode(v)
		}
	}
	return &out
}
