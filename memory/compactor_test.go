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

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

func mockSummarizer(summary string) Summarizer {
	return func(_ context.Context, _ []schema.Message) (string, error) {
		return summary, nil
	}
}

func errSummarizer(err error) Summarizer {
	return func(_ context.Context, _ []schema.Message) (string, error) {
		return "", err
	}
}

func sysMsg(text string) schema.Message {
	return schema.Message{
		Message: aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent(text)},
	}
}

func userMsg(text string) schema.Message {
	return schema.NewUserMessage(text)
}

func assistantMsg(text string) schema.Message {
	return schema.Message{
		Message: aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
	}
}

func TestConversationCompactor_NoCompactionFewMessages(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 2)

	// System + 2 pairs = 5 messages, all protected.
	msgs := []schema.Message{
		sysMsg("system prompt"),
		userMsg("hello"),
		assistantMsg("hi"),
		userMsg("how?"),
		assistantMsg("fine"),
	}

	result, tokens, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != len(msgs) {
		t.Errorf("expected %d messages (no compaction), got %d", len(msgs), len(result))
	}

	if tokens == 0 {
		t.Error("token count should be non-zero")
	}
}

func TestConversationCompactor_CompactsEligibleMessages(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("conversation summary"), 1)

	msgs := []schema.Message{
		sysMsg("system prompt"),
		userMsg("first question"),
		assistantMsg("first answer"),
		userMsg("second question"),
		assistantMsg("second answer"),
		userMsg("third question"),
		assistantMsg("third answer"),
	}

	result, _, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected: system + summary + last pair (2 messages) = 4.
	if len(result) != 4 {
		t.Errorf("expected 4 messages, got %d", len(result))
	}

	// First message should be system prompt.
	if result[0].Role != aimodel.RoleSystem {
		t.Errorf("first message role = %q, want system", result[0].Role)
	}

	if result[0].Content.Text() != "system prompt" {
		t.Errorf("first message should be original system prompt")
	}

	// Second message should be the summary.
	if result[1].Role != aimodel.RoleSystem {
		t.Errorf("summary role = %q, want system", result[1].Role)
	}

	if result[1].Content.Text() != "conversation summary" {
		t.Errorf("summary text = %q, want %q", result[1].Content.Text(), "conversation summary")
	}

	// Check metadata.
	if result[1].Metadata == nil {
		t.Fatal("summary message should have metadata")
	}

	if compressed, ok := result[1].Metadata["compressed"].(bool); !ok || !compressed {
		t.Error("metadata.compressed should be true")
	}

	if strategy, ok := result[1].Metadata["strategy"].(string); !ok || strategy != "conversation_compact" {
		t.Errorf("metadata.strategy = %q, want %q", strategy, "conversation_compact")
	}

	if sourceCount, ok := result[1].Metadata["source_count"].(int); !ok || sourceCount != 4 {
		t.Errorf("metadata.source_count = %v, want 4", sourceCount)
	}

	// Last two should be the protected pair.
	if result[2].Content.Text() != "third question" {
		t.Errorf("protected user msg = %q", result[2].Content.Text())
	}

	if result[3].Content.Text() != "third answer" {
		t.Errorf("protected assistant msg = %q", result[3].Content.Text())
	}
}

func TestConversationCompactor_NoSystemPrompt(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 1)

	msgs := []schema.Message{
		userMsg("first"),
		assistantMsg("first reply"),
		userMsg("second"),
		assistantMsg("second reply"),
		userMsg("third"),
		assistantMsg("third reply"),
	}

	result, _, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected: summary + last pair = 3.
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}

	if result[0].Role != aimodel.RoleSystem {
		t.Errorf("summary role = %q, want system", result[0].Role)
	}
}

func TestConversationCompactor_EmptyMessages(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 2)

	result, tokens, err := c.Compact(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result))
	}

	if tokens != 0 {
		t.Errorf("expected 0 tokens, got %d", tokens)
	}
}

func TestConversationCompactor_SummarizerError(t *testing.T) {
	expectedErr := errors.New("summarizer failed")
	c := NewConversationCompactor(errSummarizer(expectedErr), 1)

	msgs := []schema.Message{
		sysMsg("system"),
		userMsg("q1"),
		assistantMsg("a1"),
		userMsg("q2"),
		assistantMsg("a2"),
		userMsg("q3"),
		assistantMsg("a3"),
	}

	_, _, err := c.Compact(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "summarizer failed") {
		t.Errorf("error = %q, expected to contain %q", err.Error(), "summarizer failed")
	}
}

func TestConversationCompactor_EstimateTokens(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 2)

	msgs := []schema.Message{
		userMsg(strings.Repeat("a", 100)),      // 25 tokens
		assistantMsg(strings.Repeat("b", 200)), // 50 tokens
	}

	tokens := c.EstimateTokens(msgs)
	if tokens != 75 {
		t.Errorf("EstimateTokens = %d, want 75", tokens)
	}
}

func TestConversationCompactor_WithMaxInputTokens(t *testing.T) {
	var receivedMsgs []schema.Message
	captureSummarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
		receivedMsgs = msgs
		return "truncated summary", nil
	}

	c := NewConversationCompactor(captureSummarizer, 1).WithMaxInputTokens(10)

	// Create messages that total well over 10 tokens eligible.
	msgs := []schema.Message{
		sysMsg("system"),
		userMsg(strings.Repeat("a", 100)),      // 25 tokens
		assistantMsg(strings.Repeat("b", 100)), // 25 tokens
		userMsg(strings.Repeat("c", 100)),      // 25 tokens
		assistantMsg(strings.Repeat("d", 100)), // 25 tokens
		userMsg(strings.Repeat("e", 100)),      // 25 tokens
		assistantMsg(strings.Repeat("f", 100)), // 25 tokens
		userMsg("last"),
		assistantMsg("final"),
	}

	result, _, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have truncated the input to the summarizer.
	if len(receivedMsgs) >= 6 {
		t.Errorf("expected truncated input, got %d messages", len(receivedMsgs))
	}

	// Check an omission marker exists.
	hasMarker := false
	for _, m := range receivedMsgs {
		if strings.Contains(m.Content.Text(), "messages omitted") {
			hasMarker = true
			break
		}
	}

	if !hasMarker {
		t.Error("truncated input should contain an omission marker")
	}

	// Result should still be well-formed.
	if len(result) < 3 {
		t.Errorf("expected at least 3 messages in result, got %d", len(result))
	}
}

func TestConversationCompactor_ContextCancelled(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msgs := []schema.Message{
		userMsg("q1"),
		assistantMsg("a1"),
		userMsg("q2"),
		assistantMsg("a2"),
	}

	_, _, err := c.Compact(ctx, msgs)
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestCompactIfNeeded_BelowThreshold(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 1)

	msgs := []schema.Message{
		userMsg("short"),
	}

	result, tokens, compacted, err := CompactIfNeeded(context.Background(), c, msgs, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if compacted {
		t.Error("should not compact below threshold")
	}

	if len(result) != 1 {
		t.Errorf("expected 1 message, got %d", len(result))
	}

	if tokens == 0 {
		t.Error("tokens should be non-zero")
	}
}

func TestCompactIfNeeded_AboveThreshold(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 1)

	msgs := []schema.Message{
		sysMsg("system"),
		userMsg(strings.Repeat("a", 400)),
		assistantMsg(strings.Repeat("b", 400)),
		userMsg(strings.Repeat("c", 400)),
		assistantMsg(strings.Repeat("d", 400)),
	}

	result, _, compacted, err := CompactIfNeeded(context.Background(), c, msgs, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !compacted {
		t.Error("should compact above threshold")
	}

	if len(result) >= len(msgs) {
		t.Errorf("expected fewer messages after compaction, got %d", len(result))
	}
}

func TestCompactIfNeeded_NilCompactor(t *testing.T) {
	msgs := []schema.Message{userMsg("hello")}
	result, _, compacted, err := CompactIfNeeded(context.Background(), nil, msgs, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if compacted {
		t.Error("nil compactor should not compact")
	}

	if len(result) != 1 {
		t.Errorf("expected 1 message, got %d", len(result))
	}
}

func TestConversationCompactor_WithTokenEstimator(t *testing.T) {
	customEstimator := func(msg schema.Message) int {
		return len(msg.Content.Text()) // 1 token per char
	}

	c := NewConversationCompactor(mockSummarizer("summary"), 1).WithTokenEstimator(customEstimator)

	msgs := []schema.Message{
		userMsg("hello"),      // 5 tokens with custom
		assistantMsg("world"), // 5 tokens with custom
	}

	tokens := c.EstimateTokens(msgs)
	if tokens != 10 {
		t.Errorf("EstimateTokens = %d, want 10 (custom estimator)", tokens)
	}
}

func TestConversationCompactor_WithTokenEstimator_NilIgnored(t *testing.T) {
	c := NewConversationCompactor(mockSummarizer("summary"), 1).WithTokenEstimator(nil)

	msgs := []schema.Message{
		userMsg(strings.Repeat("a", 40)),
	}

	tokens := c.EstimateTokens(msgs)
	if tokens != 10 {
		t.Errorf("EstimateTokens = %d, want 10 (default estimator)", tokens)
	}
}

func TestConversationCompactor_ExistingSummaryMessage(t *testing.T) {
	// When history already contains a summary, it should be treated as eligible.
	c := NewConversationCompactor(mockSummarizer("new summary"), 1)

	msgs := []schema.Message{
		sysMsg("system"),
		{
			Message: aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("old summary")},
			Metadata: map[string]any{
				"compressed": true,
				"strategy":   "conversation_compact",
			},
		},
		userMsg("q1"),
		assistantMsg("a1"),
		userMsg("q2"),
		assistantMsg("a2"),
	}

	result, _, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// System + new summary + last pair = 4.
	if len(result) != 4 {
		t.Errorf("expected 4 messages, got %d", len(result))
	}
}
