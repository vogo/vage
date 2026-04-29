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

	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// TestBuilder_SessionMemory_WithSlidingWindowCompressor verifies AC-3.1 +
// AC-5.4 + the OriginalCount semantics taskagent depends on:
//
//   - The Builder integrates with a real SlidingWindowCompressor (5 messages).
//   - When 8 history messages are seeded, the output retains the most recent 5.
//   - FetchReport.OriginalCount is the *pre-compression* count (8), not the
//     post-compression count (5) — this is what TaskAgent uses as the key
//     offset for newly stored messages.
func TestBuilder_SessionMemory_WithSlidingWindowCompressor(t *testing.T) {
	sess := memory.NewSessionMemory("comp-agent", "comp-session")
	ctx := context.Background()

	const seed = 8
	const window = 5

	for i := range seed {
		key := fmt.Sprintf("msg:%06d", i)
		text := fmt.Sprintf("turn-%02d", i)
		if err := sess.Set(ctx, key, schema.NewUserMessage(text), 0); err != nil {
			t.Fatalf("seed Set %d: %v", i, err)
		}
	}

	mm := memory.NewManager(
		memory.WithSession(sess),
		memory.WithCompressor(memory.NewSlidingWindowCompressor(window)),
	)

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("Sys.")}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: mm}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: "comp-session",
		Request: &schema.RunRequest{
			SessionID: "comp-session",
			Messages:  []schema.Message{schema.NewUserMessage("now")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Locate session_memory report.
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

	if memRep.OriginalCount != seed {
		t.Errorf("OriginalCount = %d, want %d (pre-compression)", memRep.OriginalCount, seed)
	}
	if memRep.OutputN != window {
		t.Errorf("OutputN = %d, want %d (post-compression)", memRep.OutputN, window)
	}
	if memRep.DroppedN != seed-window {
		t.Errorf("DroppedN = %d, want %d", memRep.DroppedN, seed-window)
	}

	// Verify the surviving messages are the latest five (turn-03 .. turn-07).
	// res.Messages: [system, history (5), request] = 7 total.
	if len(res.Messages) != 1+window+1 {
		t.Fatalf("len(messages) = %d, want %d", len(res.Messages), 1+window+1)
	}
	for i := range window {
		want := fmt.Sprintf("turn-%02d", seed-window+i)
		got := res.Messages[1+i].Content.Text()
		if got != want {
			t.Errorf("history[%d] = %q, want %q", i, got, want)
		}
	}
}
