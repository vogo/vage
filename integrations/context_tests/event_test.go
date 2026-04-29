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
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// TestTaskAgent_EmitsEventContextBuilt covers AC-2.2: when TaskAgent runs
// with a hook manager wired in, EventContextBuilt must fire and its
// payload must be a populated ContextBuiltData (i.e. the BuildReport
// translated to event form, not a zero-value struct).
func TestTaskAgent_EmitsEventContextBuilt(t *testing.T) {
	rec := newRecordingHook()
	hm := installHook(rec)

	fake := &fakeChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	a := taskagent.New(agent.Config{ID: "evt-agent"},
		taskagent.WithChatCompleter(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Sys.")),
		taskagent.WithHookManager(hm),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "evt-session",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := rec.byType(schema.EventContextBuilt)
	if len(events) != 1 {
		t.Fatalf("got %d EventContextBuilt events, want 1", len(events))
	}

	evt := events[0]
	if evt.AgentID != "evt-agent" {
		t.Errorf("event AgentID = %q, want %q", evt.AgentID, "evt-agent")
	}
	if evt.SessionID != "evt-session" {
		t.Errorf("event SessionID = %q, want %q", evt.SessionID, "evt-session")
	}

	data, ok := evt.Data.(schema.ContextBuiltData)
	if !ok {
		t.Fatalf("event Data type = %T, want schema.ContextBuiltData", evt.Data)
	}

	if data.Builder != vctx.DefaultBuilderName {
		t.Errorf("Builder = %q, want %q", data.Builder, vctx.DefaultBuilderName)
	}
	if data.Strategy != vctx.StrategyOrderedGreedy {
		t.Errorf("Strategy = %q, want %q", data.Strategy, vctx.StrategyOrderedGreedy)
	}
	// System prompt + request user message → at least 2 output messages.
	if data.OutputCount < 2 {
		t.Errorf("OutputCount = %d, want >= 2", data.OutputCount)
	}
	if data.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %d, want > 0", data.OutputTokens)
	}
	if len(data.Sources) == 0 {
		t.Errorf("Sources is empty, want at least 1 entry")
	}

	// Per-source presence: system_prompt and request_messages must report
	// "ok"; session_memory may report "skipped" because no memory manager
	// was wired in.
	seen := make(map[string]schema.ContextSourceReport)
	for _, s := range data.Sources {
		seen[s.Source] = s
	}

	if r, ok := seen[vctx.SourceNameSystemPrompt]; !ok {
		t.Errorf("missing source report: %s", vctx.SourceNameSystemPrompt)
	} else if r.Status != vctx.StatusOK {
		t.Errorf("system_prompt status = %q, want %q", r.Status, vctx.StatusOK)
	}

	if r, ok := seen[vctx.SourceNameRequestMessages]; !ok {
		t.Errorf("missing source report: %s", vctx.SourceNameRequestMessages)
	} else if r.Status != vctx.StatusOK {
		t.Errorf("request_messages status = %q, want %q", r.Status, vctx.StatusOK)
	}
}
