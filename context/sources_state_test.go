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
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/session"
)

// seedStore returns a freshly created MapSessionStore with the given
// session id and key/value pairs already populated. It centralises the
// boilerplate so each test focuses on Source behaviour.
func seedStore(t *testing.T, sessionID string, kv map[string]any) *session.MapSessionStore {
	t.Helper()

	store := session.NewMapSessionStore()
	if err := store.Create(context.Background(), session.New(sessionID)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for k, v := range kv {
		if err := store.SetState(context.Background(), sessionID, k, v); err != nil {
			t.Fatalf("SetState %q: %v", k, err)
		}
	}
	return store
}

// TestSessionStateSource_RenderKeys verifies the default renderer prints
// keys in declared order, and only the keys that exist appear.
func TestSessionStateSource_RenderKeys(t *testing.T) {
	store := seedStore(t, "s1", map[string]any{
		"plan":  "do X",
		"phase": 2,
	})

	src := &SessionStateSource{
		Store: store,
		Keys:  []string{"plan", "phase", "missing"},
	}

	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	if res.Messages[0].Role != aimodel.RoleSystem {
		t.Errorf("Role = %q, want system", res.Messages[0].Role)
	}
	text := res.Messages[0].Content.Text()
	if !strings.Contains(text, "plan: do X") {
		t.Errorf("missing plan: %q", text)
	}
	if !strings.Contains(text, "phase: 2") {
		t.Errorf("missing phase: %q", text)
	}
	if strings.Contains(text, "missing") {
		t.Errorf("rendered missing key: %q", text)
	}
	// Order: plan before phase (configured order).
	if strings.Index(text, "plan") > strings.Index(text, "phase") {
		t.Errorf("key order wrong: %q", text)
	}
}

// TestSessionStateSource_NoStore verifies a nil store / empty keys / empty
// session id all produce a skip.
func TestSessionStateSource_NoStore(t *testing.T) {
	cases := []struct {
		name string
		src  *SessionStateSource
		in   FetchInput
	}{
		{
			name: "nil store",
			src:  &SessionStateSource{Keys: []string{"a"}},
			in:   FetchInput{SessionID: "s"},
		},
		{
			name: "empty keys",
			src:  &SessionStateSource{Store: session.NewMapSessionStore()},
			in:   FetchInput{SessionID: "s"},
		},
		{
			name: "empty session id",
			src: &SessionStateSource{
				Store: session.NewMapSessionStore(),
				Keys:  []string{"a"},
			},
			in: FetchInput{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.src.Fetch(context.Background(), c.in)
			if err != nil {
				t.Fatalf("Fetch returned error: %v", err)
			}
			if len(res.Messages) != 0 {
				t.Errorf("expected 0 messages, got %d", len(res.Messages))
			}
			if res.Report.Status != StatusSkipped {
				t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
			}
		})
	}
}

// TestSessionStateSource_AllMissing verifies the source reports skip
// (not error) when every requested key is absent.
func TestSessionStateSource_AllMissing(t *testing.T) {
	store := seedStore(t, "s1", nil)
	src := &SessionStateSource{
		Store: store,
		Keys:  []string{"a", "b"},
	}

	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestSessionStateSource_CustomRender verifies a user-supplied renderer
// is called and its output ends up in the message.
func TestSessionStateSource_CustomRender(t *testing.T) {
	store := seedStore(t, "s1", map[string]any{"x": 1})
	src := &SessionStateSource{
		Store: store,
		Keys:  []string{"x"},
		Render: func(_ map[string]any) string {
			return "<custom-render-output>"
		},
	}

	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if res.Messages[0].Content.Text() != "<custom-render-output>" {
		t.Errorf("render not respected: %q", res.Messages[0].Content.Text())
	}
}

// TestSessionStateSource_RendererEmpty verifies a custom renderer that
// returns "" produces a skip rather than emitting an empty message.
func TestSessionStateSource_RendererEmpty(t *testing.T) {
	store := seedStore(t, "s1", map[string]any{"x": 1})
	src := &SessionStateSource{
		Store:  store,
		Keys:   []string{"x"},
		Render: func(_ map[string]any) string { return "" },
	}

	res, err := src.Fetch(context.Background(), FetchInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}
