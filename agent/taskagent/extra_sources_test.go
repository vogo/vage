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

package taskagent

import (
	"context"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/schema"
)

// markerSource emits a single system message containing a recognisable
// marker string. We use it to assert that WithExtraSources slots into the
// message stream between SessionMemory and Request.
type markerSource struct {
	marker string
}

func (m *markerSource) Name() string { return "marker_" + m.marker }
func (m *markerSource) Fetch(_ context.Context, _ vctx.FetchInput) (vctx.FetchResult, error) {
	return vctx.FetchResult{
		Messages: []aimodel.Message{
			{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("MARKER:" + m.marker)},
		},
		Report: schema.ContextSourceReport{
			Source:  "marker_" + m.marker,
			Status:  vctx.StatusOK,
			OutputN: 1,
		},
	}, nil
}

// TestWithExtraSources_Order asserts the canonical message order
// [system, session_memory(empty), ...extras, request] is preserved when
// extra sources are plugged in via WithExtraSources.
func TestWithExtraSources_Order(t *testing.T) {
	a := New(agent.Config{ID: "t1"},
		WithExtraSources(&markerSource{marker: "alpha"}, &markerSource{marker: "beta"}),
	)

	br, err := a.buildInitialMessages(context.Background(), &schema.RunRequest{
		SessionID: "sess",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("buildInitialMessages: %v", err)
	}

	// Expected: [system?(empty since systemPrompt=nil produces nothing),
	//            session_memory(empty),
	//            marker_alpha,
	//            marker_beta,
	//            user("hi")]
	// SystemPromptSource emits nothing when Template is nil; SessionMemorySource
	// emits nothing when memoryManager is nil — so the surviving sequence is
	// [marker_alpha, marker_beta, user("hi")].
	if got := len(br.messages); got != 3 {
		t.Fatalf("messages = %d, want 3 — got %+v", got, br.messages)
	}
	if got := br.messages[0].Content.Text(); got != "MARKER:alpha" {
		t.Errorf("messages[0] = %q, want MARKER:alpha", got)
	}
	if got := br.messages[1].Content.Text(); got != "MARKER:beta" {
		t.Errorf("messages[1] = %q, want MARKER:beta", got)
	}
	if got := br.messages[2].Role; got != aimodel.RoleUser {
		t.Errorf("messages[2].Role = %q, want user", got)
	}
}

// TestWithExtraSources_NilSkipped ensures that calling WithExtraSources(nil)
// or passing a nil entry doesn't panic and is a no-op.
func TestWithExtraSources_NilSkipped(t *testing.T) {
	a := New(agent.Config{ID: "t1"},
		WithExtraSources(nil, &markerSource{marker: "only"}, nil),
	)
	if got := len(a.extraSources); got != 1 {
		t.Errorf("extraSources len = %d, want 1 (nils filtered)", got)
	}
}

// TestBuildInitialMessages_NoExtras_BehaviourCompat re-asserts the original
// hand-rolled order: with no extras, the message stream is the same as
// before WithExtraSources existed (empty SystemPrompt + empty session +
// request).
func TestBuildInitialMessages_NoExtras_BehaviourCompat(t *testing.T) {
	a := New(agent.Config{ID: "t1"})
	br, err := a.buildInitialMessages(context.Background(), &schema.RunRequest{
		SessionID: "sess",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("buildInitialMessages: %v", err)
	}
	// Only the user message should remain — SystemPromptSource (nil
	// template) and SessionMemorySource (nil manager) both skip.
	if len(br.messages) != 1 {
		t.Fatalf("messages = %d, want 1: %+v", len(br.messages), br.messages)
	}
	if br.messages[0].Role != aimodel.RoleUser {
		t.Errorf("role = %q, want user", br.messages[0].Role)
	}
}
