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

package context_tests

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session"
)

// TestSessionStateSource_EndToEnd validates AC-4.1: the SessionStateSource
// (the design's example "non-trivial" source) plugs into a real
// session.MapSessionStore, reads selected keys, and contributes a single
// system message to the Builder output. The session is created via the
// real Store API; values come back through GetState exactly as written.
func TestSessionStateSource_EndToEnd(t *testing.T) {
	store := session.NewMapSessionStore()
	ctx := context.Background()

	const sid = "state-session"

	if err := store.Create(ctx, &session.Session{ID: sid}); err != nil {
		t.Fatalf("Create session: %v", err)
	}
	if err := store.SetState(ctx, sid, "user_name", "alice"); err != nil {
		t.Fatalf("SetState user_name: %v", err)
	}
	if err := store.SetState(ctx, sid, "language", "en-US"); err != nil {
		t.Fatalf("SetState language: %v", err)
	}

	stateSrc := &vctx.SessionStateSource{
		Store: store,
		Keys:  []string{"user_name", "language"},
	}

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("Sys.")}),
		vctx.WithSource(stateSrc),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: sid,
		Request: &schema.RunRequest{
			SessionID: sid,
			Messages:  []schema.Message{schema.NewUserMessage("hi")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Output: [system, state-projection (system role), request].
	if len(res.Messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(res.Messages))
	}

	stateMsg := res.Messages[1]
	if stateMsg.Role != aimodel.RoleSystem {
		t.Errorf("state message Role = %q, want %q", stateMsg.Role, aimodel.RoleSystem)
	}
	body := stateMsg.Content.Text()
	if !strings.Contains(body, "user_name: alice") {
		t.Errorf("state message missing 'user_name: alice'; got %q", body)
	}
	if !strings.Contains(body, "language: en-US") {
		t.Errorf("state message missing 'language: en-US'; got %q", body)
	}

	// Confirm the state source's report came through with InputN = 2.
	var found bool
	for _, s := range res.Report.Sources {
		if s.Source == vctx.SourceNameSessionState {
			found = true
			if s.Status != vctx.StatusOK {
				t.Errorf("session_state Status = %q, want %q", s.Status, vctx.StatusOK)
			}
			if s.InputN != 2 {
				t.Errorf("session_state InputN = %d, want 2", s.InputN)
			}
			if s.OutputN != 1 {
				t.Errorf("session_state OutputN = %d, want 1", s.OutputN)
			}
		}
	}
	if !found {
		t.Errorf("session_state missing from BuildReport.Sources")
	}
}

// TestSessionStateSource_MissingKeysAndNoSession verifies the source's
// fail-open / skip behaviour when nothing useful exists yet:
//   - empty session id  → Status="skipped"
//   - keys not present  → Status="skipped" with note "no state to render"
func TestSessionStateSource_MissingKeysAndNoSession(t *testing.T) {
	store := session.NewMapSessionStore()
	ctx := context.Background()

	// Create the session but populate no state.
	if err := store.Create(ctx, &session.Session{ID: "bare"}); err != nil {
		t.Fatalf("Create session: %v", err)
	}

	src := &vctx.SessionStateSource{Store: store, Keys: []string{"absent"}}

	// Case 1: empty SessionID → skipped via the "no session" guard.
	res, err := src.Fetch(ctx, vctx.FetchInput{SessionID: ""})
	if err != nil {
		t.Fatalf("Fetch empty session: %v", err)
	}
	if res.Report.Status != vctx.StatusSkipped {
		t.Errorf("empty SessionID Status = %q, want %q", res.Report.Status, vctx.StatusSkipped)
	}
	if len(res.Messages) != 0 {
		t.Errorf("empty SessionID emitted %d messages, want 0", len(res.Messages))
	}

	// Case 2: keys missing → skipped with "no state to render".
	res, err = src.Fetch(ctx, vctx.FetchInput{SessionID: "bare"})
	if err != nil {
		t.Fatalf("Fetch missing keys: %v", err)
	}
	if res.Report.Status != vctx.StatusSkipped {
		t.Errorf("missing keys Status = %q, want %q", res.Report.Status, vctx.StatusSkipped)
	}
	if len(res.Messages) != 0 {
		t.Errorf("missing keys emitted %d messages, want 0", len(res.Messages))
	}
}
