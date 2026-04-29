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
	"fmt"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// TestTaskAgent_ContextAssembly_BehaviorCompat verifies that running a
// TaskAgent end-to-end with a system prompt + memoryManager + a user
// message produces exactly the same prompt order the legacy
// buildInitialMessages produced before the Builder refactor:
//
//	[ system, history (oldest → newest), current request ]
//
// AC-3.1: SystemPromptSource + SessionMemorySource together replicate
// prior taskagent behaviour.
func TestTaskAgent_ContextAssembly_BehaviorCompat(t *testing.T) {
	// Pre-seed the session memory with three turns so the Builder has
	// historical messages to fold in.
	sess := memory.NewSessionMemory("compat-agent", "compat-session")

	ctx := context.Background()
	for i, text := range []string{"first turn", "second turn", "third turn"} {
		key := fmt.Sprintf("msg:%06d", i)
		if err := sess.Set(ctx, key, schema.NewUserMessage(text), 0); err != nil {
			t.Fatalf("seed Set %d: %v", i, err)
		}
	}

	mm := memory.NewManager(memory.WithSession(sess))

	fake := &fakeChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("done")}}

	a := taskagent.New(agent.Config{ID: "compat-agent", Name: "Compat"},
		taskagent.WithChatCompleter(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Be helpful.")),
		taskagent.WithMemory(mm),
	)

	_, err := a.Run(ctx, &schema.RunRequest{
		SessionID: "compat-session",
		Messages:  []schema.Message{schema.NewUserMessage("current question")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.firstRequest(t)
	got := req.Messages

	// Expect: 1 system + 3 history + 1 current = 5 messages, in that order.
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(got), describeMessages(got))
	}

	// 0: system
	if got[0].Role != aimodel.RoleSystem {
		t.Errorf("messages[0].Role = %q, want %q", got[0].Role, aimodel.RoleSystem)
	}
	if got[0].Content.Text() != "Be helpful." {
		t.Errorf("messages[0].Content = %q, want %q", got[0].Content.Text(), "Be helpful.")
	}

	// 1..3: history in oldest-first order.
	want := []string{"first turn", "second turn", "third turn"}
	for i, w := range want {
		idx := 1 + i
		if got[idx].Role != aimodel.RoleUser {
			t.Errorf("messages[%d].Role = %q, want %q", idx, got[idx].Role, aimodel.RoleUser)
		}
		if got[idx].Content.Text() != w {
			t.Errorf("messages[%d].Content = %q, want %q", idx, got[idx].Content.Text(), w)
		}
	}

	// 4: current request
	if got[4].Role != aimodel.RoleUser {
		t.Errorf("messages[4].Role = %q, want %q", got[4].Role, aimodel.RoleUser)
	}
	if got[4].Content.Text() != "current question" {
		t.Errorf("messages[4].Content = %q, want %q", got[4].Content.Text(), "current question")
	}
}

// TestTaskAgent_PromptCacheBreakpointPreserved exercises AC-3.4: the
// prompt-cache breakpoint must still be marked on the system message
// after the Builder runs (markPromptCacheBreakpoints is the post-step).
func TestTaskAgent_PromptCacheBreakpointPreserved(t *testing.T) {
	fake := &fakeChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	a := taskagent.New(agent.Config{ID: "cache-agent"},
		taskagent.WithChatCompleter(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Sys.")),
		taskagent.WithPromptCaching(true),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "cache-session",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.firstRequest(t)

	if len(req.Messages) == 0 {
		t.Fatalf("no messages captured")
	}
	if req.Messages[0].Role != aimodel.RoleSystem {
		t.Fatalf("first message is not system: %+v", req.Messages[0])
	}
	if !req.Messages[0].CacheBreakpoint {
		t.Errorf("system message CacheBreakpoint = false, want true")
	}
}

// describeMessages formats a message slice as a list of (role, snippet)
// tuples for failure messages.
func describeMessages(msgs []aimodel.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = fmt.Sprintf("(%s, %q)", m.Role, m.Content.Text())
	}
	return out
}
