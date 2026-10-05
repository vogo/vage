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

// Package memory implements two LLM-facing tools over a caller-supplied
// Store:
//
//   - memory_set    — write or delete one durable fact (op=set|delete);
//   - memory_recall — list facts by namespace / key prefix / limit.
//
// The package does not import vage/memory (L2 tool must not depend on L1
// state). Access control belongs to the Store implementation — vv stamps
// agent-path identity onto context before the call reaches the backend.
// Shared vs session-private namespace names follow the MEM-R2 convention
// so recall with an empty namespace returns cross-session facts rather
// than session scratch.
package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vogo/largemodel/schema"
	"github.com/vogo/vage/tool"
)

// Tool names. Stable identifiers used by registries, permission gates,
// and prompt/tool contract tests.
const (
	SetToolName    = "memory_set"
	RecallToolName = "memory_recall"
)

// EventSet is the CustomEventData.Name emitted after a successful
// memory_set op=set. Payload is map[string]any with keys namespace, key,
// shared — never value.
const EventSet = "memory.set"

// EventDelete is the CustomEventData.Name emitted after a successful
// memory_set op=delete. Payload is map[string]any with keys namespace,
// key, shared — never value.
const EventDelete = "memory.delete"

// Size / result caps keep a single tool call from dominating the
// context window. 16 KiB matches vector_add; 20/50 match typical
// recall pages rather than dumping the whole store.
const (
	MaxValueBytes = 16 * 1024
	MaxIdentBytes = 256
	DefaultLimit  = 20
	MaxLimit      = 50
)

// SharedNamespaceNames is the MEM-R2 shared-namespace enumeration.
// Names not in this set are treated as session-private by recall when
// the caller omits namespace (empty namespace = all shared).
var SharedNamespaceNames = []string{
	"project",
	"user",
	"conventions",
	"notes",
	"default",
}

var sharedNamespaceSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(SharedNamespaceNames))
	for _, n := range SharedNamespaceNames {
		m[n] = struct{}{}
	}
	return m
}()

// IsSharedNamespace reports whether ns is a cross-session shared name.
func IsSharedNamespace(ns string) bool {
	_, ok := sharedNamespaceSet[ns]
	return ok
}

var (
	errStoreRequired = errors.New("memory: store is required")
	errRegistryNil   = errors.New("memory: registry is nil")
)

// Store is the persistence seam the tools need. It is intentionally
// smaller than vage/memory.Memory so this L2 package does not import L1.
type Store interface {
	Get(ctx context.Context, key string) (any, error)
	Set(ctx context.Context, key string, value any, ttl int64) error
	// Delete removes one logical key. A missing key returns nil.
	// Implementations apply their own session ACL. This package never
	// calls a clear-all operation.
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]Entry, error)
}

// Entry is one recalled fact. Key is the logical namespace:name (or a
// bare name in the default namespace).
type Entry struct {
	Key   string
	Value any
}

// Register installs both memory tools onto reg. All-or-nothing: a
// partial registration would let the LLM see memory_set but not
// memory_recall, encouraging it to write facts it cannot read back.
func Register(reg *tool.Registry, store Store) error {
	if reg == nil {
		return errRegistryNil
	}
	if store == nil {
		return errStoreRequired
	}

	st := newSetTool(store)
	if err := reg.RegisterIfAbsent(st.ToolDef(), st.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", SetToolName, err)
	}
	rt := newRecallTool(store)
	if err := reg.RegisterIfAbsent(rt.ToolDef(), rt.Handler()); err != nil {
		return fmt.Errorf("register %s: %w", RecallToolName, err)
	}
	return nil
}

func errResult(toolName string, err error) schema.ToolResult {
	return schema.ErrorResult("", toolName+": "+err.Error())
}

func logicalKey(namespace, key string) string {
	if namespace == "" || namespace == "default" {
		return key
	}
	return namespace + ":" + key
}

func splitLogicalKey(key string) (namespace, name string) {
	if before, after, ok := strings.Cut(key, ":"); ok {
		return before, after
	}
	return "default", key
}

func validIdent(s string) bool {
	if s == "" || len(s) > MaxIdentBytes {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func clipOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 240
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "..."
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
