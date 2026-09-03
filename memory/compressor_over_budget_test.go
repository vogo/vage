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

package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func longTurn(i int) []schema.Message {
	body := strings.Repeat("x", 40) // ~10 tokens
	user := schema.NewUserMessage(schema.ProtocolOpenAIChat, "u"+string(rune('0'+i))+body)
	asst := schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "a"+string(rune('0'+i))+body)
	return []schema.Message{user, asst}
}

func concatTurns(n int) []schema.Message {
	var out []schema.Message
	for i := range n {
		out = append(out, longTurn(i)...)
	}
	return out
}

func TestSummarizeWhenOverBudget_UnderBudgetNoCall(t *testing.T) {
	called := false
	sum := func(context.Context, []schema.Message) (string, error) {
		called = true
		return "SUMMARY", nil
	}
	c := SummarizeWhenOverBudget(sum, KeepRecentTurns(1), TargetUtilization(0.8))
	msgs := concatTurns(2)
	// Plenty of room.
	out, err := c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 10_000, AvailableHistory: 10_000},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if called {
		t.Fatal("summarizer should not run under budget")
	}
	if len(out) != len(msgs) {
		t.Fatalf("len=%d, want %d", len(out), len(msgs))
	}
}

func TestSummarizeWhenOverBudget_UnlimitedAndZeroRemaining(t *testing.T) {
	called := false
	sum := func(context.Context, []schema.Message) (string, error) {
		called = true
		return "SUMMARY", nil
	}
	c := SummarizeWhenOverBudget(sum, KeepRecentTurns(1))
	msgs := concatTurns(3)

	out, err := c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{},
	})
	if err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	if called || len(out) != len(msgs) {
		t.Fatalf("unlimited should return original (called=%v len=%d)", called, len(out))
	}

	out, err = c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 100, AvailableHistory: 0},
	})
	if err != nil {
		t.Fatalf("zero remaining: %v", err)
	}
	if called {
		t.Fatal("summarizer must not run on zero remaining")
	}
	if len(out) != 0 {
		t.Fatalf("zero remaining should be empty, got %d", len(out))
	}
}

func TestSummarizeWhenOverBudget_KeepsRecentTurnsAndSummarizesPrefix(t *testing.T) {
	var older []schema.Message
	sum := func(_ context.Context, msgs []schema.Message) (string, error) {
		older = append([]schema.Message(nil), msgs...)
		return "SUMMARY_OF_OLD", nil
	}
	c := SummarizeWhenOverBudget(sum, KeepRecentTurns(1), TargetUtilization(0.8))
	msgs := concatTurns(3) // 6 messages, last turn is turn 2

	out, err := c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 80, AvailableHistory: 40},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(older) != 4 {
		t.Fatalf("older prefix len=%d, want 4 (first two turns)", len(older))
	}
	if len(out) != 3 { // summary + last user + last assistant
		t.Fatalf("out len=%d, want 3", len(out))
	}
	if out[0].Text() != "SUMMARY_OF_OLD" {
		t.Errorf("summary text = %q", out[0].Text())
	}
	if strat, _ := out[0].Metadata["strategy"].(string); strat != "summarize_when_over_budget" {
		t.Errorf("strategy=%v", out[0].Metadata["strategy"])
	}
	if out[1].Text() != msgs[4].Text() || out[2].Text() != msgs[5].Text() {
		t.Errorf("recent turns not preserved: %q / %q", out[1].Text(), out[2].Text())
	}
}

func TestSummarizeWhenOverBudget_RecentOverTargetKeepsTurns(t *testing.T) {
	c := SummarizeWhenOverBudget(func(context.Context, []schema.Message) (string, error) {
		return "SHOULD_NOT_SPLIT_TURNS", nil
	}, KeepRecentTurns(2), TargetUtilization(0.8))
	msgs := concatTurns(3)
	out, err := c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 50, AvailableHistory: 5},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("should keep 2 complete turns (4 msgs), got %d", len(out))
	}
	if out[0].Role() != schema.RoleUser {
		t.Errorf("recent should still start at a user turn, role=%s", out[0].Role())
	}
}

func TestSummarizeWhenOverBudget_SummarizerError(t *testing.T) {
	c := SummarizeWhenOverBudget(func(context.Context, []schema.Message) (string, error) {
		return "", errors.New("summarizer failed")
	}, KeepRecentTurns(1))
	_, err := c.CompressWithBudget(context.Background(), CompressionInput{
		Messages: concatTurns(2),
		Budget:   Budget{ModelContextTokens: 40, AvailableHistory: 30},
	})
	if err == nil || !strings.Contains(err.Error(), "summarizer failed") {
		t.Fatalf("want summarizer error, got %v", err)
	}
}

func TestSummarizeWhenOverBudget_RejectsIllegalOptions(t *testing.T) {
	sum := func(context.Context, []schema.Message) (string, error) { return "s", nil }

	mustPanic(t, "nil summarizer", func() { SummarizeWhenOverBudget(nil, KeepRecentTurns(1)) })
	mustPanic(t, "missing keep", func() { SummarizeWhenOverBudget(sum) })
	mustPanic(t, "zero keep", func() { SummarizeWhenOverBudget(sum, KeepRecentTurns(0)) })
	mustPanic(t, "util 0", func() { SummarizeWhenOverBudget(sum, KeepRecentTurns(1), TargetUtilization(0)) })
	mustPanic(t, "util >1", func() { SummarizeWhenOverBudget(sum, KeepRecentTurns(1), TargetUtilization(1.1)) })
}

func TestSummarizeWhenOverBudget_LegacyCompressEntry(t *testing.T) {
	called := false
	c := SummarizeWhenOverBudget(func(context.Context, []schema.Message) (string, error) {
		called = true
		return "S", nil
	}, KeepRecentTurns(1))
	msgs := concatTurns(2)
	out, err := c.Compress(context.Background(), msgs, 0)
	if err != nil {
		t.Fatalf("unlimited compress: %v", err)
	}
	if called || len(out) != len(msgs) {
		t.Fatalf("Compress(...,0) should be unlimited")
	}
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	fn()
}
