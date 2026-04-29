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
	"strings"
	"testing"

	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// TestBuilder_Budget_OldestDropped verifies AC-5.1, AC-5.2: when the
// session-history source overshoots the budget, the Builder's trim
// fallback drops the oldest message(s) first and records the dropped
// count.
//
// Construction:
//   - 6 user messages of 40 chars each in the session memory tier.
//   - Each message ≈ 40/4 = 10 tokens (memory.EstimateTextTokens uses a
//     "1 token per 4 characters" heuristic).
//   - System prompt + request user message together stay small (~few
//     tokens) so most of the Budget is available for history.
//   - Total budget set such that only the newest 3 history messages fit.
func TestBuilder_Budget_OldestDropped(t *testing.T) {
	sess := memory.NewSessionMemory("budget", "budget-session")
	ctx := context.Background()

	body := strings.Repeat("x", 40) // ~10 tokens each
	for i := range 6 {
		key := fmt.Sprintf("msg:%06d", i)
		text := fmt.Sprintf("%02d:%s", i, body)
		if err := sess.Set(ctx, key, schema.NewUserMessage(text), 0); err != nil {
			t.Fatalf("seed Set %d: %v", i, err)
		}
	}
	mm := memory.NewManager(memory.WithSession(sess))

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("S")}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: mm}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	// Budget = 35 tokens. System prompt ("S") ≈ 1 token, request message
	// ("q") ≈ 1 token → ~33 tokens left for history → ~3 of 6 history
	// messages survive.
	budget := 35

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: "budget-session",
		Budget:    budget,
		Request: &schema.RunRequest{
			SessionID: "budget-session",
			Messages:  []schema.Message{schema.NewUserMessage("q")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Locate the session-memory source report.
	var memRep *schema.ContextSourceReport
	for i := range res.Report.Sources {
		s := &res.Report.Sources[i]
		if s.Source == vctx.SourceNameSessionMemory {
			memRep = s
		}
	}
	if memRep == nil {
		t.Fatalf("missing session_memory report")
	}

	if memRep.DroppedN <= 0 {
		t.Errorf("expected DroppedN > 0, got %d (status=%q tokens=%d)",
			memRep.DroppedN, memRep.Status, memRep.Tokens)
	}
	if memRep.Status != vctx.StatusTruncated {
		t.Errorf("session_memory status = %q, want %q", memRep.Status, vctx.StatusTruncated)
	}
	if memRep.OriginalCount != 6 {
		t.Errorf("OriginalCount = %d, want 6", memRep.OriginalCount)
	}
	if memRep.OutputN >= 6 {
		t.Errorf("OutputN = %d, want < 6 (some dropped)", memRep.OutputN)
	}
	if res.Report.DroppedCount != memRep.DroppedN {
		t.Errorf("BuildReport.DroppedCount = %d, want %d (sum of source droppedN)",
			res.Report.DroppedCount, memRep.DroppedN)
	}

	// Surviving history messages should be the *newest* (highest index
	// keys come last after key-sort). Inspect the actual messages: the
	// system prompt is messages[0]; history starts at index 1.
	if len(res.Messages) < 2 {
		t.Fatalf("not enough messages: %d", len(res.Messages))
	}

	// First surviving history message must NOT be the oldest seeded one —
	// the oldest ("00:...") is the one budget trim drops first.
	firstHist := res.Messages[1].Content.Text()
	if strings.HasPrefix(firstHist, "00:") {
		t.Errorf("oldest message survived trim; first history msg = %q", firstHist)
	}

	// Total output tokens must respect the budget.
	if res.Report.OutputTokens > budget {
		t.Errorf("OutputTokens = %d, exceeds Budget = %d", res.Report.OutputTokens, budget)
	}
}

// TestBuilder_BudgetZero_Unlimited verifies AC-5.3: a Budget of 0 is
// treated as unlimited — no trimming, no DroppedCount, all history
// messages survive.
func TestBuilder_BudgetZero_Unlimited(t *testing.T) {
	sess := memory.NewSessionMemory("nolimit", "nolimit-session")
	ctx := context.Background()
	body := strings.Repeat("x", 80) // ~20 tokens each

	for i := range 6 {
		key := fmt.Sprintf("msg:%06d", i)
		text := fmt.Sprintf("%02d:%s", i, body)
		if err := sess.Set(ctx, key, schema.NewUserMessage(text), 0); err != nil {
			t.Fatalf("seed Set %d: %v", i, err)
		}
	}
	mm := memory.NewManager(memory.WithSession(sess))

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("S")}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: mm}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: "nolimit-session",
		Budget:    0,
		Request: &schema.RunRequest{
			SessionID: "nolimit-session",
			Messages:  []schema.Message{schema.NewUserMessage("q")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if res.Report.DroppedCount != 0 {
		t.Errorf("DroppedCount = %d, want 0 (unlimited budget)", res.Report.DroppedCount)
	}
	// 1 system + 6 history + 1 request = 8.
	if res.Report.OutputCount != 8 {
		t.Errorf("OutputCount = %d, want 8", res.Report.OutputCount)
	}
	if res.Report.InputBudget != 0 {
		t.Errorf("InputBudget = %d, want 0", res.Report.InputBudget)
	}
}
