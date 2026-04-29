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
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// DefaultSessionMemoryPrefix is the key prefix SessionMemorySource scans by
// default. It matches taskagent's historical convention so the source is a
// drop-in replacement for the previous loadAndCompressSessionHistory path.
const DefaultSessionMemoryPrefix = "msg:"

// SessionMemorySource loads ordered messages from
// memory.Manager.Session() (the conversation-scoped tier), optionally
// applies the manager's compressor, and emits the resulting message slice.
//
// FetchReport.OriginalCount is filled with the pre-compression count, which
// TaskAgent uses as the index offset for newly stored messages.
type SessionMemorySource struct {
	Manager *memory.Manager
	// Prefix overrides DefaultSessionMemoryPrefix. Empty means "use default".
	Prefix string
}

// Compile-time interface conformance.
var _ Source = (*SessionMemorySource)(nil)

// Name returns SourceNameSessionMemory.
func (s *SessionMemorySource) Name() string { return SourceNameSessionMemory }

// Fetch loads, sorts, and compresses session-tier messages. List failures
// and compressor failures are both fail-open: a slog.Warn is emitted, the
// report is annotated, and the source returns whatever messages it could
// recover (possibly none).
func (s *SessionMemorySource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameSessionMemory}

	if s.Manager == nil || s.Manager.Session() == nil {
		rep.Status = StatusSkipped
		rep.Note = "no session memory"
		return FetchResult{Report: rep}, nil
	}

	prefix := s.Prefix
	if prefix == "" {
		prefix = DefaultSessionMemoryPrefix
	}

	loaded, err := s.loadOrdered(ctx, prefix)
	if err != nil {
		// Fail-open: warn, mark report, return empty.
		slog.Warn("vctx: load session messages", "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	originalCount := len(loaded)
	rep.OriginalCount = originalCount
	rep.InputN = originalCount

	if originalCount == 0 {
		rep.Status = StatusSkipped
		return FetchResult{Report: rep}, nil
	}

	if c := s.Manager.Compressor(); c != nil {
		compressed, compErr := c.Compress(ctx, loaded, 0)
		if compErr != nil {
			slog.Warn("vctx: compress session messages", "error", compErr)
			// Fall through with the uncompressed slice; do not flip Status
			// to error because we still produced output.
			rep.Note = "compressor failed; uncompressed"
		} else {
			loaded = compressed
		}
	}

	out := schema.ToAIModelMessages(loaded)

	if len(out) < originalCount {
		rep.DroppedN = originalCount - len(out)
	}
	rep.OutputN = len(out)
	if len(out) == 0 {
		rep.Status = StatusSkipped
		rep.Note = "compressed to empty"
	} else {
		rep.Status = StatusOK
	}
	// Tokens left at zero — Builder fills via estimator.

	return FetchResult{Messages: out, Report: rep}, nil
}

// loadOrdered fetches every entry under prefix, sorts by key, and converts
// to schema.Message. Entries whose value is not a schema.Message are
// skipped with a slog.Warn (matching prior taskagent behaviour). The
// caller is responsible for treating any error as fail-open.
func (s *SessionMemorySource) loadOrdered(ctx context.Context, prefix string) ([]schema.Message, error) {
	entries, err := s.Manager.Session().List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("vctx: list session: %w", err)
	}

	if len(entries) == 0 {
		return nil, nil
	}

	slices.SortFunc(entries, func(a, b memory.Entry) int {
		return strings.Compare(a.Key, b.Key)
	})

	msgs := make([]schema.Message, 0, len(entries))
	for _, e := range entries {
		msg, ok := e.Value.(schema.Message)
		if !ok {
			slog.Warn("vctx: unexpected entry type in session",
				"key", e.Key, "type", fmt.Sprintf("%T", e.Value))
			continue
		}
		msgs = append(msgs, msg)
	}

	return msgs, nil
}
