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
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session"
)

// StateRenderer turns a key/value snapshot into a string suitable for a
// system message. Returning "" makes SessionStateSource emit a skipped
// status.
type StateRenderer func(map[string]any) string

// SessionStateSource projects selected keys from a session.SessionStateStore
// into a single system-role message. It exists to demonstrate how the
// Source interface plugs into the session entity's structured state KV —
// future plan / scratchpad / tree sources will follow the same shape.
type SessionStateSource struct {
	Store  session.SessionStateStore
	Keys   []string
	Render StateRenderer
}

// Compile-time interface conformance.
var _ Source = (*SessionStateSource)(nil)

// Name returns SourceNameSessionState.
func (s *SessionStateSource) Name() string { return SourceNameSessionState }

// Fetch reads each requested key from the store, falls through missing or
// errored keys (fail-open per key), and renders the surviving key/value
// snapshot to a single system message.
func (s *SessionStateSource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameSessionState}

	if s.Store == nil || len(s.Keys) == 0 || in.SessionID == "" {
		rep.Status = StatusSkipped
		rep.Note = "no store / no keys / no session"
		return FetchResult{Report: rep}, nil
	}

	rep.InputN = len(s.Keys)
	state := make(map[string]any, len(s.Keys))
	failed := 0

	for _, k := range s.Keys {
		v, ok, err := s.Store.GetState(ctx, in.SessionID, k)
		if err != nil {
			failed++
			slog.Warn("vctx: session state get",
				"key", k, "session_id", in.SessionID, "error", err)
			continue
		}

		if ok {
			state[k] = v
		}
	}

	if failed == len(s.Keys) {
		rep.Status = StatusError
		rep.Error = "all GetState calls failed"
		return FetchResult{Report: rep}, nil
	}

	if len(state) == 0 {
		rep.Status = StatusSkipped
		rep.Note = "no state to render"
		return FetchResult{Report: rep}, nil
	}

	render := s.Render
	if render == nil {
		render = defaultStateRender(s.Keys)
	}

	text := render(state)
	if text == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty render"
		return FetchResult{Report: rep}, nil
	}

	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}

	rep.Status = StatusOK
	rep.OutputN = 1

	return FetchResult{Messages: []aimodel.Message{msg}, Report: rep}, nil
}

// defaultStateRender returns a StateRenderer that prints "<key>: <value>"
// lines in the order configured on the source. Values are rendered with
// %v; producing a sensible string for complex values is the caller's job
// (use a custom Render if needed).
func defaultStateRender(orderedKeys []string) StateRenderer {
	return func(state map[string]any) string {
		var b strings.Builder
		first := true
		for _, k := range orderedKeys {
			v, ok := state[k]
			if !ok {
				continue
			}
			if !first {
				b.WriteByte('\n')
			}
			first = false
			fmt.Fprintf(&b, "%s: %v", k, v)
		}
		return b.String()
	}
}
