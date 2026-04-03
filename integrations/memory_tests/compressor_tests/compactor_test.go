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

package compressor_tests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// =============================================================================
// Helper Functions for Compactor Integration Tests
// =============================================================================

// buildConversation creates a realistic conversation with a system prompt,
// multiple user/assistant turn pairs, and interleaved tool messages.
// Returns the message slice and the count of user/assistant pairs.
func buildConversation(turnCount int) []schema.Message {
	msgs := []schema.Message{
		{
			Message:   aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("You are a helpful coding assistant.")},
			Timestamp: time.Now(),
		},
	}

	for i := 1; i <= turnCount; i++ {
		// User message.
		msgs = append(msgs, schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(fmt.Sprintf("User question %d: %s", i, strings.Repeat("x", 40)))},
			Timestamp: time.Now(),
		})

		// Occasionally add a tool call/result pair (every 3rd turn).
		if i%3 == 0 {
			msgs = append(msgs, schema.Message{
				Message: aimodel.Message{
					Role:    aimodel.RoleAssistant,
					Content: aimodel.NewTextContent(fmt.Sprintf("Let me look that up (turn %d)...", i)),
					ToolCalls: []aimodel.ToolCall{
						{ID: fmt.Sprintf("call-%d", i), Function: aimodel.FunctionCall{Name: "search", Arguments: "{}"}},
					},
				},
				Timestamp: time.Now(),
			})
			msgs = append(msgs, schema.Message{
				Message:   aimodel.Message{Role: aimodel.RoleTool, Content: aimodel.NewTextContent(fmt.Sprintf("Tool result for turn %d: found relevant info", i))},
				Timestamp: time.Now(),
			})
		}

		// Assistant response.
		msgs = append(msgs, schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(fmt.Sprintf("Assistant answer %d: %s", i, strings.Repeat("y", 60)))},
			Timestamp: time.Now(),
		})
	}

	return msgs
}

// mockCompactSummarizer returns a deterministic summarizer that echoes the
// count of input messages (no LLM needed).
func mockCompactSummarizer() memory.Summarizer {
	return func(_ context.Context, msgs []schema.Message) (string, error) {
		return fmt.Sprintf("Summary of %d messages: The conversation covered coding topics, tool usage, and Q&A.", len(msgs)), nil
	}
}

// =============================================================================
// Integration Test: ConversationCompactor with realistic conversation
// =============================================================================

// TestIntegration_ConversationCompactor_RealisticConversation verifies that the
// ConversationCompactor correctly compresses a realistic conversation with 20+
// messages (mix of user, assistant, tool), preserving the system prompt and
// protected turn pairs while producing a valid summary message with metadata.
func TestIntegration_ConversationCompactor_RealisticConversation(t *testing.T) {
	msgs := buildConversation(12) // 12 turns = system + 12 user + some tool + 12 assistant = 20+ messages

	if len(msgs) < 20 {
		t.Fatalf("expected at least 20 messages in test conversation, got %d", len(msgs))
	}

	protectedTurns := 2
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), protectedTurns)

	result, resultTokens, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Verify system prompt is preserved as the first message.
	if result[0].Role != aimodel.RoleSystem {
		t.Errorf("first message role = %q, want system", result[0].Role)
	}
	if result[0].Content.Text() != "You are a helpful coding assistant." {
		t.Errorf("system prompt not preserved: %q", result[0].Content.Text())
	}

	// Verify a summary message exists with compressed metadata and RoleSystem.
	foundSummary := false
	for _, m := range result {
		if m.Metadata != nil {
			if compressed, ok := m.Metadata["compressed"].(bool); ok && compressed {
				foundSummary = true

				if m.Role != aimodel.RoleSystem {
					t.Errorf("summary message role = %q, want system", m.Role)
				}

				if strategy, ok := m.Metadata["strategy"].(string); !ok || strategy != "conversation_compact" {
					t.Errorf("summary strategy = %q, want %q", strategy, "conversation_compact")
				}

				if sourceCount, ok := m.Metadata["source_count"].(int); !ok || sourceCount == 0 {
					t.Errorf("summary source_count = %v, want > 0", sourceCount)
				}

				if !strings.Contains(m.Content.Text(), "Summary of") {
					t.Errorf("summary content does not contain expected text: %q", m.Content.Text())
				}

				break
			}
		}
	}
	if !foundSummary {
		t.Fatal("no summary message found in compacted result")
	}

	// Verify last 2 user/assistant pairs are preserved verbatim.
	// The last messages should be the protected tail.
	lastUser := ""
	lastAssistant := ""
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role == aimodel.RoleAssistant && lastAssistant == "" {
			lastAssistant = result[i].Content.Text()
		}
		if result[i].Role == aimodel.RoleUser && lastUser == "" {
			lastUser = result[i].Content.Text()
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}

	if !strings.Contains(lastUser, "User question 12") {
		t.Errorf("last protected user message not preserved: %q", lastUser)
	}
	if !strings.Contains(lastAssistant, "Assistant answer 12") {
		t.Errorf("last protected assistant message not preserved: %q", lastAssistant)
	}

	// Verify total token count is reduced.
	originalTokens := compactor.EstimateTokens(msgs)
	if resultTokens >= originalTokens {
		t.Errorf("compacted tokens (%d) should be less than original (%d)", resultTokens, originalTokens)
	}

	// Verify result has fewer messages than original.
	if len(result) >= len(msgs) {
		t.Errorf("compacted message count (%d) should be less than original (%d)", len(result), len(msgs))
	}

	t.Logf("Original: %d messages (%d tokens), Compacted: %d messages (%d tokens)",
		len(msgs), originalTokens, len(result), resultTokens)
}

// =============================================================================
// Integration Test: ConversationCompactor with multiple protected turns
// =============================================================================

// TestIntegration_ConversationCompactor_ProtectedTurnCounts verifies that
// different protectedTurns values correctly preserve the expected number of
// recent turn pairs in the compacted output.
func TestIntegration_ConversationCompactor_ProtectedTurnCounts(t *testing.T) {
	msgs := buildConversation(8) // 8 turns

	tests := []struct {
		name           string
		protectedTurns int
	}{
		{name: "protect 1 turn", protectedTurns: 1},
		{name: "protect 2 turns", protectedTurns: 2},
		{name: "protect 3 turns", protectedTurns: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compactor := memory.NewConversationCompactor(mockCompactSummarizer(), tt.protectedTurns)
			result, _, err := compactor.Compact(context.Background(), msgs)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}

			// Count user/assistant pairs at the end of the result (after the summary).
			pairsAfterSummary := 0
			for i := len(result) - 1; i >= 0; i-- {
				if result[i].Metadata != nil {
					if compressed, ok := result[i].Metadata["compressed"].(bool); ok && compressed {
						break
					}
				}
				if result[i].Role == aimodel.RoleUser {
					pairsAfterSummary++
				}
			}

			if pairsAfterSummary != tt.protectedTurns {
				t.Errorf("protected user messages = %d, want %d", pairsAfterSummary, tt.protectedTurns)
			}
		})
	}
}

// =============================================================================
// Integration Test: ConversationCompactor re-compaction of existing summary
// =============================================================================

// TestIntegration_ConversationCompactor_ReCompaction verifies that a
// conversation that already contains a summary message from a previous
// compaction can be compacted again, treating the old summary as eligible.
func TestIntegration_ConversationCompactor_ReCompaction(t *testing.T) {
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1)

	// Build initial conversation and compact once.
	msgs := buildConversation(6)
	firstResult, _, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("first Compact: %v", err)
	}

	// Add more messages after the first compaction.
	firstResult = append(firstResult,
		schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("New question after compaction")},
			Timestamp: time.Now(),
		},
		schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("New answer after compaction")},
			Timestamp: time.Now(),
		},
		schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("Another question")},
			Timestamp: time.Now(),
		},
		schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("Another answer")},
			Timestamp: time.Now(),
		},
	)

	// Compact again -- the old summary should be included in eligible messages.
	secondResult, _, err := compactor.Compact(context.Background(), firstResult)
	if err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	// Verify the result is well-formed.
	if len(secondResult) >= len(firstResult) {
		t.Errorf("re-compacted count (%d) should be < input count (%d)", len(secondResult), len(firstResult))
	}

	// Verify system prompt still present.
	if secondResult[0].Role != aimodel.RoleSystem && secondResult[0].Content.Text() != "You are a helpful coding assistant." {
		t.Error("system prompt should be preserved after re-compaction")
	}

	// Verify exactly one summary message exists.
	summaryCount := 0
	for _, m := range secondResult {
		if m.Metadata != nil {
			if compressed, ok := m.Metadata["compressed"].(bool); ok && compressed {
				summaryCount++
			}
		}
	}
	if summaryCount != 1 {
		t.Errorf("expected exactly 1 summary message after re-compaction, got %d", summaryCount)
	}

	t.Logf("After re-compaction: %d messages (from %d)", len(secondResult), len(firstResult))
}

// =============================================================================
// Integration Test: CompactIfNeeded threshold behavior
// =============================================================================

// TestIntegration_CompactIfNeeded_ThresholdBehavior verifies that
// CompactIfNeeded correctly triggers compaction only when the estimated
// token count exceeds the threshold, and returns the correct compacted flag.
func TestIntegration_CompactIfNeeded_ThresholdBehavior(t *testing.T) {
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1)

	// Build a conversation with known token count.
	msgs := buildConversation(10) // substantial conversation
	originalTokens := compactor.EstimateTokens(msgs)

	// Test: threshold above token count -- no compaction.
	t.Run("below threshold no compaction", func(t *testing.T) {
		highThreshold := originalTokens + 10000
		result, tokens, compacted, err := memory.CompactIfNeeded(context.Background(), compactor, msgs, highThreshold)
		if err != nil {
			t.Fatalf("CompactIfNeeded: %v", err)
		}
		if compacted {
			t.Error("should not compact below threshold")
		}
		if len(result) != len(msgs) {
			t.Errorf("message count changed: got %d, want %d", len(result), len(msgs))
		}
		if tokens != originalTokens {
			t.Errorf("token count = %d, want %d", tokens, originalTokens)
		}
	})

	// Test: threshold below token count -- compaction occurs.
	t.Run("above threshold triggers compaction", func(t *testing.T) {
		lowThreshold := 10 // very low threshold, guaranteed to trigger
		result, tokens, compacted, err := memory.CompactIfNeeded(context.Background(), compactor, msgs, lowThreshold)
		if err != nil {
			t.Fatalf("CompactIfNeeded: %v", err)
		}
		if !compacted {
			t.Error("should compact above threshold")
		}
		if len(result) >= len(msgs) {
			t.Errorf("compacted count (%d) should be < original (%d)", len(result), len(msgs))
		}
		if tokens >= originalTokens {
			t.Errorf("compacted tokens (%d) should be < original (%d)", tokens, originalTokens)
		}
	})

	// Test: nil compactor -- no compaction, no error.
	t.Run("nil compactor returns original", func(t *testing.T) {
		result, _, compacted, err := memory.CompactIfNeeded(context.Background(), nil, msgs, 1)
		if err != nil {
			t.Fatalf("CompactIfNeeded: %v", err)
		}
		if compacted {
			t.Error("nil compactor should not compact")
		}
		if len(result) != len(msgs) {
			t.Errorf("message count = %d, want %d", len(result), len(msgs))
		}
	})
}

// =============================================================================
// Integration Test: ConversationCompactor with WithMaxInputTokens
// =============================================================================

// TestIntegration_ConversationCompactor_MaxInputTokensTruncation verifies
// that WithMaxInputTokens correctly limits the input to the summarizer,
// inserting an omission marker when the eligible messages exceed the budget.
func TestIntegration_ConversationCompactor_MaxInputTokensTruncation(t *testing.T) {
	var receivedMessages []schema.Message
	capturingSummarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
		receivedMessages = make([]schema.Message, len(msgs))
		copy(receivedMessages, msgs)
		return fmt.Sprintf("Truncated summary of %d input messages", len(msgs)), nil
	}

	// Use a very small maxInputTokens to force truncation.
	compactor := memory.NewConversationCompactor(capturingSummarizer, 1).
		WithMaxInputTokens(20) // very small limit

	// Build a large conversation with many eligible messages.
	msgs := buildConversation(10)
	originalEligible := len(msgs) - 3 // system + last pair excluded

	result, _, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Verify summarizer received fewer messages than the full eligible set.
	if len(receivedMessages) >= originalEligible {
		t.Errorf("summarizer received %d messages, expected less than %d (truncated)", len(receivedMessages), originalEligible)
	}

	// Verify an omission marker is present in the summarizer input.
	hasOmissionMarker := false
	for _, m := range receivedMessages {
		if strings.Contains(m.Content.Text(), "messages omitted") {
			hasOmissionMarker = true
			break
		}
	}
	if !hasOmissionMarker {
		t.Error("truncated summarizer input should contain an omission marker")
	}

	// Verify result is still well-formed.
	if len(result) < 3 {
		t.Errorf("expected at least 3 messages in result (system + summary + protected), got %d", len(result))
	}
}

// =============================================================================
// Integration Test: ConversationCompactor with no system prompt
// =============================================================================

// TestIntegration_ConversationCompactor_NoSystemPrompt verifies that
// compaction works correctly when the conversation has no system prompt,
// producing a summary without a leading system message.
func TestIntegration_ConversationCompactor_NoSystemPrompt(t *testing.T) {
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1)

	// Build conversation without system prompt.
	msgs := []schema.Message{}
	for i := 1; i <= 8; i++ {
		msgs = append(msgs, schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(fmt.Sprintf("Q%d: %s", i, strings.Repeat("a", 40)))},
			Timestamp: time.Now(),
		})
		msgs = append(msgs, schema.Message{
			Message:   aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(fmt.Sprintf("A%d: %s", i, strings.Repeat("b", 60)))},
			Timestamp: time.Now(),
		})
	}

	result, _, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// First message should be the summary (no system prompt prefix).
	if result[0].Role != aimodel.RoleSystem {
		t.Errorf("first message role = %q, want system (summary)", result[0].Role)
	}
	if result[0].Metadata == nil {
		t.Fatal("first message should be the summary with metadata")
	}
	if compressed, ok := result[0].Metadata["compressed"].(bool); !ok || !compressed {
		t.Error("first message should have compressed=true metadata")
	}

	// Should have summary + protected pair (1 turn = 2 msgs) = 3 messages.
	if len(result) != 3 {
		t.Errorf("expected 3 messages (summary + 1 protected pair), got %d", len(result))
	}
}

// =============================================================================
// Integration Test: ConversationCompactor preserves custom token estimator
// =============================================================================

// TestIntegration_ConversationCompactor_CustomTokenEstimator verifies that
// a custom token estimator is used throughout compaction, affecting both
// threshold checking and the result token count.
func TestIntegration_ConversationCompactor_CustomTokenEstimator(t *testing.T) {
	// Custom estimator: 1 token per character (inflated vs default).
	customEstimator := func(msg schema.Message) int {
		text := msg.Content.Text()
		if len(text) == 0 {
			return 0
		}
		return len(text)
	}

	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1).
		WithTokenEstimator(customEstimator)

	msgs := buildConversation(6)

	// Verify token estimate uses custom estimator.
	customTokens := compactor.EstimateTokens(msgs)
	defaultCompactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1)
	defaultTokens := defaultCompactor.EstimateTokens(msgs)

	// Custom (1 per char) should give ~4x the default (1 per 4 chars).
	if customTokens <= defaultTokens {
		t.Errorf("custom tokens (%d) should be > default tokens (%d)", customTokens, defaultTokens)
	}

	// Compact should still work.
	result, resultTokens, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if resultTokens >= customTokens {
		t.Errorf("compacted tokens (%d) should be < original (%d)", resultTokens, customTokens)
	}
	if len(result) >= len(msgs) {
		t.Errorf("compacted count (%d) should be < original (%d)", len(result), len(msgs))
	}
}

// =============================================================================
// Integration Test: ConversationCompactor concurrent safety
// =============================================================================

// TestIntegration_ConversationCompactor_ConcurrentSafety verifies that
// ConversationCompactor is safe for concurrent use (read-only struct).
func TestIntegration_ConversationCompactor_ConcurrentSafety(t *testing.T) {
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 2)
	msgs := buildConversation(8)

	const goroutines = 10
	errs := make(chan error, goroutines)

	for range goroutines {
		go func() {
			_, _, err := compactor.Compact(context.Background(), msgs)
			errs <- err
		}()
	}

	for range goroutines {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Compact error: %v", err)
		}
	}
}

// =============================================================================
// Integration Test: ConversationCompactor emergency compaction (protectedTurns=1)
// =============================================================================

// TestIntegration_ConversationCompactor_EmergencyCompaction verifies that
// emergency compaction (protectedTurns=1) aggressively compresses the
// conversation, keeping only the system prompt, summary, and the very last
// turn pair.
func TestIntegration_ConversationCompactor_EmergencyCompaction(t *testing.T) {
	compactor := memory.NewConversationCompactor(mockCompactSummarizer(), 1)
	msgs := buildConversation(10)

	result, _, err := compactor.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Expected: system + summary + 1 user + 1 assistant = 4 messages.
	if len(result) != 4 {
		t.Errorf("emergency compaction should produce 4 messages, got %d", len(result))
	}

	// Verify the last user message is from the last turn.
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role == aimodel.RoleUser {
			if !strings.Contains(result[i].Content.Text(), "User question 10") {
				t.Errorf("last protected user = %q, want to contain 'User question 10'", result[i].Content.Text())
			}
			break
		}
	}
}
