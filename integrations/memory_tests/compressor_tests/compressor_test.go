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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/memory"
	"github.com/vogo/vagent/schema"
)

// =============================================================================
// Helper Functions
// =============================================================================

func newSystemMessage(text string) schema.Message {
	return schema.Message{
		Message:   aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent(text)},
		Timestamp: time.Now(),
	}
}

func newAssistantMessage(text string) schema.Message {
	return schema.Message{
		Message:   aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
		Timestamp: time.Now(),
	}
}

func newToolMessage(text string) schema.Message {
	return schema.Message{
		Message:   aimodel.Message{Role: aimodel.RoleTool, Content: aimodel.NewTextContent(text)},
		Timestamp: time.Now(),
	}
}

func newAssistantWithToolCalls(text string) schema.Message {
	return schema.Message{
		Message: aimodel.Message{
			Role:    aimodel.RoleAssistant,
			Content: aimodel.NewTextContent(text),
			ToolCalls: []aimodel.ToolCall{
				{ID: "call-1", Function: aimodel.FunctionCall{Name: "test_tool", Arguments: "{}"}},
			},
		},
		Timestamp: time.Now(),
	}
}

// =============================================================================
// Integration Test: ContextCompressor Interface Compliance
// =============================================================================

// TestIntegration_AllCompressors_InterfaceCompliance verifies that all four
// compressor implementations satisfy the ContextCompressor interface and
// share consistent behavior for common edge cases.
func TestIntegration_AllCompressors_InterfaceCompliance(t *testing.T) {
	mockSummarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
		return fmt.Sprintf("summary of %d messages", len(msgs)), nil
	}

	compressors := map[string]memory.ContextCompressor{
		"SlidingWindow":     memory.NewSlidingWindowCompressor(10),
		"TokenBudget":       memory.NewTokenBudgetCompressor(),
		"SummarizeAndTrunc": memory.NewSummarizeAndTruncCompressor(mockSummarizer, 5),
		"ImportanceRanking": memory.NewImportanceRankingCompressorWithDefaults(),
	}

	// Test: empty input returns empty output for all compressors.
	t.Run("empty input returns empty output", func(t *testing.T) {
		for name, c := range compressors {
			t.Run(name, func(t *testing.T) {
				result, err := c.Compress(context.Background(), []schema.Message{}, 100)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != 0 {
					t.Errorf("got %d messages, want 0", len(result))
				}
			})
		}
	})

	// Test: cancelled context returns error for all compressors.
	t.Run("cancelled context returns error", func(t *testing.T) {
		for name, c := range compressors {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				msgs := []schema.Message{schema.NewUserMessage("hello")}
				_, err := c.Compress(ctx, msgs, 100)
				if err == nil {
					t.Fatal("expected error for cancelled context")
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want context.Canceled", err)
				}
			})
		}
	})

	// Test: single message input returns non-empty output for all compressors.
	t.Run("single message returns non-empty output", func(t *testing.T) {
		for name, c := range compressors {
			t.Run(name, func(t *testing.T) {
				msgs := []schema.Message{schema.NewUserMessage("hello world")}
				result, err := c.Compress(context.Background(), msgs, 100)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) == 0 {
					t.Error("expected at least one message in output")
				}
			})
		}
	})

	// Test: unlimited budget (maxTokens=0) returns messages for all compressors.
	t.Run("unlimited budget preserves messages", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"),
			schema.NewUserMessage("bbbb"),
			schema.NewUserMessage("cccc"),
		}
		for name, c := range compressors {
			t.Run(name, func(t *testing.T) {
				result, err := c.Compress(context.Background(), msgs, 0)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) == 0 {
					t.Error("expected non-empty output with unlimited budget")
				}
			})
		}
	})
}

// =============================================================================
// Integration Test: Compressor Chaining (Pipeline Pattern)
// =============================================================================

// TestIntegration_CompressorChaining tests applying multiple compressors
// sequentially, simulating a compression pipeline where output of one
// feeds into the next.
func TestIntegration_CompressorChaining(t *testing.T) {
	// Test: SlidingWindow then TokenBudget — first reduces by count, then by tokens.
	t.Run("SlidingWindow then TokenBudget", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"),                  // 1 token
			schema.NewUserMessage("bbbb"),                  // 1 token
			schema.NewUserMessage("cccc"),                  // 1 token
			schema.NewUserMessage(strings.Repeat("d", 40)), // 10 tokens
			schema.NewUserMessage("eeee"),                  // 1 token
			schema.NewUserMessage(strings.Repeat("f", 20)), // 5 tokens
			schema.NewUserMessage("gggg"),                  // 1 token
		}

		// Step 1: SlidingWindow keeps last 5
		sw := memory.NewSlidingWindowCompressor(5)
		step1, err := sw.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("SlidingWindow error: %v", err)
		}
		if len(step1) != 5 {
			t.Fatalf("after SlidingWindow: got %d, want 5", len(step1))
		}

		// Step 2: TokenBudget trims to fit within 7 tokens
		tb := memory.NewTokenBudgetCompressor()
		step2, err := tb.Compress(context.Background(), step1, 7)
		if err != nil {
			t.Fatalf("TokenBudget error: %v", err)
		}
		// Last 3 messages: eeee(1) + f*20(5) + gggg(1) = 7 tokens
		if len(step2) != 3 {
			t.Fatalf("after TokenBudget: got %d, want 3", len(step2))
		}
		if step2[0].Content.Text() != "eeee" {
			t.Errorf("step2[0] = %q, want %q", step2[0].Content.Text(), "eeee")
		}
		if step2[2].Content.Text() != "gggg" {
			t.Errorf("step2[2] = %q, want %q", step2[2].Content.Text(), "gggg")
		}
	})

	// Test: ImportanceRanking then SummarizeAndTrunc — first prunes low-importance,
	// then summarizes remaining older messages.
	t.Run("ImportanceRanking then SummarizeAndTrunc", func(t *testing.T) {
		msgs := []schema.Message{
			newSystemMessage("system prompt"),            // high priority
			schema.NewUserMessage("user question 1"),     // medium
			newAssistantMessage(strings.Repeat("a", 80)), // low, 20 tokens
			schema.NewUserMessage("user question 2"),     // medium
			newAssistantMessage("short answer"),          // low
			schema.NewUserMessage("user question 3"),     // medium
		}

		// Step 1: ImportanceRanking with budget that drops long assistant message
		ir := memory.NewImportanceRankingCompressorWithDefaults()
		step1, err := ir.Compress(context.Background(), msgs, 10)
		if err != nil {
			t.Fatalf("ImportanceRanking error: %v", err)
		}
		// Should have dropped the 20-token assistant message
		if len(step1) < 3 {
			t.Fatalf("after ImportanceRanking: got %d, want >= 3", len(step1))
		}

		// Step 2: SummarizeAndTrunc keeps last 2 messages verbatim
		summarizer := func(_ context.Context, older []schema.Message) (string, error) {
			return fmt.Sprintf("Summary of %d previous messages", len(older)), nil
		}
		st := memory.NewSummarizeAndTruncCompressor(summarizer, 2)
		step2, err := st.Compress(context.Background(), step1, 0)
		if err != nil {
			t.Fatalf("SummarizeAndTrunc error: %v", err)
		}
		if len(step2) < 2 {
			t.Fatalf("after SummarizeAndTrunc: got %d, want >= 2", len(step2))
		}

		// Verify the first message is a summary (if more than keepLastN were input)
		if len(step1) > 2 {
			if step2[0].Metadata == nil {
				t.Fatal("expected summary message to have metadata")
			}
			if compressed, ok := step2[0].Metadata["compressed"].(bool); !ok || !compressed {
				t.Error("expected compressed=true in summary metadata")
			}
		}
	})
}

// =============================================================================
// Integration Test: CompressFunc Adapter with Real Compressor
// =============================================================================

// TestIntegration_CompressFunc_Wrapping tests the CompressFunc adapter
// wrapping real compressor logic, verifying it satisfies ContextCompressor.
func TestIntegration_CompressFunc_Wrapping(t *testing.T) {
	// Wrap a custom compression strategy as a CompressFunc.
	var compressor memory.ContextCompressor = memory.CompressFunc(
		func(ctx context.Context, msgs []schema.Message, maxTokens int) ([]schema.Message, error) {
			// First apply token budget
			tb := memory.NewTokenBudgetCompressor()
			result, err := tb.Compress(ctx, msgs, maxTokens)
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	)

	msgs := []schema.Message{
		schema.NewUserMessage("aaaa"),     // 1 token
		schema.NewUserMessage("bbbbbbbb"), // 2 tokens
		schema.NewUserMessage("cccc"),     // 1 token
	}

	result, err := compressor.Compress(context.Background(), msgs, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Budget=3: last 2 (2+1=3)
	if len(result) != 2 {
		t.Fatalf("got %d messages, want 2", len(result))
	}
}

// =============================================================================
// Integration Test: SlidingWindow with MaxTokens (US-1 Acceptance Criteria)
// =============================================================================

// TestIntegration_SlidingWindow_MaxTokens_EndToEnd verifies that SlidingWindow
// correctly applies both window size and token budget limits simultaneously.
func TestIntegration_SlidingWindow_MaxTokens_EndToEnd(t *testing.T) {
	// Test: window trims first, then token budget trims further.
	t.Run("window and token budget interact correctly", func(t *testing.T) {
		c := memory.NewSlidingWindowCompressor(4)
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"),                  // 1 token
			schema.NewUserMessage(strings.Repeat("b", 20)), // 5 tokens
			schema.NewUserMessage("cccc"),                  // 1 token
			schema.NewUserMessage(strings.Repeat("d", 20)), // 5 tokens
			schema.NewUserMessage("eeee"),                  // 1 token
			schema.NewUserMessage("ffff"),                  // 1 token
		}
		// Window=4 keeps last 4: cccc(1), d*20(5), eeee(1), ffff(1)
		// Budget=2 keeps last: eeee(1) + ffff(1) = 2
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Content.Text() != "eeee" {
			t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "eeee")
		}
		if result[1].Content.Text() != "ffff" {
			t.Errorf("result[1] = %q, want %q", result[1].Content.Text(), "ffff")
		}
	})

	// Test: all window messages fit within budget.
	t.Run("all window messages fit budget", func(t *testing.T) {
		c := memory.NewSlidingWindowCompressor(3)
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"), // 1 token
			schema.NewUserMessage("bbbb"), // 1 token
			schema.NewUserMessage("cccc"), // 1 token
			schema.NewUserMessage("dddd"), // 1 token
			schema.NewUserMessage("eeee"), // 1 token
		}
		// Window=3, budget=100: last 3 messages (3 tokens, well under budget)
		result, err := c.Compress(context.Background(), msgs, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	// Test: oversized single message is always returned.
	t.Run("oversized single message always returned", func(t *testing.T) {
		c := memory.NewSlidingWindowCompressor(5)
		msgs := []schema.Message{
			schema.NewUserMessage(strings.Repeat("x", 200)), // 50 tokens
		}
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatal("expected at least the most recent message")
		}
	})
}

// =============================================================================
// Integration Test: TokenBudget with Diverse Message Types (US-2 Acceptance)
// =============================================================================

// TestIntegration_TokenBudget_DiverseMessages verifies TokenBudgetCompressor
// works correctly with system, user, assistant, and tool messages of varying sizes.
func TestIntegration_TokenBudget_DiverseMessages(t *testing.T) {
	c := memory.NewTokenBudgetCompressor()

	// Test: conversation with mixed roles — budget keeps most recent that fit.
	t.Run("mixed role conversation under budget pressure", func(t *testing.T) {
		msgs := []schema.Message{
			newSystemMessage("You are a helpful assistant"),       // ~8 tokens
			schema.NewUserMessage("What is the weather?"),         // ~5 tokens
			newAssistantMessage("Let me check the weather tool."), // ~8 tokens
			newToolMessage("Weather: sunny, 25C"),                 // ~5 tokens
			newAssistantMessage("The weather is sunny and 25C."),  // ~8 tokens
			schema.NewUserMessage("Thanks!"),                      // ~1 token
		}

		// Budget of 10 should keep the last few messages
		result, err := c.Compress(context.Background(), msgs, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) == 0 {
			t.Fatal("expected at least one message")
		}
		// Last message should always be present
		if result[len(result)-1].Content.Text() != "Thanks!" {
			t.Errorf("last message = %q, want %q", result[len(result)-1].Content.Text(), "Thanks!")
		}
		// Should be fewer messages than input
		if len(result) >= len(msgs) {
			t.Errorf("expected fewer messages than input (%d), got %d", len(msgs), len(result))
		}
	})

	// Test: all messages fit — no truncation.
	t.Run("all messages fit within budget", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"), // 1 token
			schema.NewUserMessage("bbbb"), // 1 token
		}
		result, err := c.Compress(context.Background(), msgs, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
	})
}

// =============================================================================
// Integration Test: SummarizeAndTrunc End-to-End (US-3 Acceptance Criteria)
// =============================================================================

// TestIntegration_SummarizeAndTrunc_EndToEnd tests the full summarize-and-truncate
// flow including summary message metadata, role assignment, and conversation structure.
func TestIntegration_SummarizeAndTrunc_EndToEnd(t *testing.T) {
	// Test: realistic conversation summarization.
	t.Run("realistic multi-turn conversation", func(t *testing.T) {
		var summarizedMsgs []schema.Message
		summarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
			summarizedMsgs = msgs
			var parts []string
			for _, m := range msgs {
				parts = append(parts, fmt.Sprintf("[%s]: %s", m.Role, m.Content.Text()))
			}
			return "Previous conversation: " + strings.Join(parts, "; "), nil
		}

		c := memory.NewSummarizeAndTruncCompressor(summarizer, 3)
		msgs := []schema.Message{
			newSystemMessage("You are helpful"),
			schema.NewUserMessage("Hello"),
			newAssistantMessage("Hi there!"),
			schema.NewUserMessage("What's 2+2?"),
			newAssistantMessage("4"),
			schema.NewUserMessage("And 3+3?"),
			newAssistantMessage("6"),
		}

		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have 1 summary + 3 recent = 4 messages
		if len(result) != 4 {
			t.Fatalf("got %d messages, want 4", len(result))
		}

		// Verify summarizer received the older messages (first 4)
		if len(summarizedMsgs) != 4 {
			t.Fatalf("summarizer received %d messages, want 4", len(summarizedMsgs))
		}

		// Verify summary message structure
		summary := result[0]
		if summary.Role != aimodel.RoleUser {
			t.Errorf("summary role = %q, want %q", summary.Role, aimodel.RoleUser)
		}
		if !strings.Contains(summary.Content.Text(), "Previous conversation") {
			t.Error("summary should contain 'Previous conversation'")
		}
		if summary.Metadata == nil {
			t.Fatal("summary metadata should not be nil")
		}
		if compressed, ok := summary.Metadata["compressed"].(bool); !ok || !compressed {
			t.Error("expected compressed=true")
		}
		if sourceCount, ok := summary.Metadata["source_count"].(int); !ok || sourceCount != 4 {
			t.Errorf("expected source_count=4, got %v", summary.Metadata["source_count"])
		}
		if strategy, ok := summary.Metadata["strategy"].(string); !ok || strategy != "summarize_and_trunc" {
			t.Errorf("expected strategy=summarize_and_trunc, got %v", summary.Metadata["strategy"])
		}

		// Verify recent messages preserved (last 3 of 7: "4", "And 3+3?", "6")
		if result[1].Content.Text() != "4" {
			t.Errorf("result[1] = %q, want %q", result[1].Content.Text(), "4")
		}
		if result[2].Content.Text() != "And 3+3?" {
			t.Errorf("result[2] = %q, want %q", result[2].Content.Text(), "And 3+3?")
		}
		if result[3].Content.Text() != "6" {
			t.Errorf("result[3] = %q, want %q", result[3].Content.Text(), "6")
		}
	})

	// Test: custom summary role (e.g., system).
	t.Run("custom summary role as system message", func(t *testing.T) {
		summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "Context summary", nil
		}
		c := memory.NewSummarizeAndTruncCompressor(summarizer, 1, memory.WithSummaryRole(aimodel.RoleSystem))

		msgs := []schema.Message{
			schema.NewUserMessage("old message 1"),
			schema.NewUserMessage("old message 2"),
			schema.NewUserMessage("current question"),
		}

		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Role != aimodel.RoleSystem {
			t.Errorf("summary role = %q, want %q", result[0].Role, aimodel.RoleSystem)
		}
	})

	// Test: summarizer returns error — propagated correctly.
	t.Run("summarizer error propagation", func(t *testing.T) {
		expectedErr := errors.New("LLM API unavailable")
		summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "", expectedErr
		}
		c := memory.NewSummarizeAndTruncCompressor(summarizer, 1)
		msgs := []schema.Message{
			schema.NewUserMessage("old"),
			schema.NewUserMessage("new"),
		}

		_, err := c.Compress(context.Background(), msgs, 0)
		if err == nil {
			t.Fatal("expected error from summarizer")
		}
		if !errors.Is(err, expectedErr) {
			t.Errorf("error = %v, want %v", err, expectedErr)
		}
	})

	// Test: summarizer returns empty string — no summary message created.
	t.Run("empty summary produces no summary message", func(t *testing.T) {
		summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "", nil
		}
		c := memory.NewSummarizeAndTruncCompressor(summarizer, 2)
		msgs := []schema.Message{
			schema.NewUserMessage("old 1"),
			schema.NewUserMessage("old 2"),
			schema.NewUserMessage("recent 1"),
			schema.NewUserMessage("recent 2"),
		}

		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Only recent messages, no summary
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Content.Text() != "recent 1" {
			t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "recent 1")
		}
	})
}

// =============================================================================
// Integration Test: ImportanceRanking with Realistic Conversations (US-4)
// =============================================================================

// TestIntegration_ImportanceRanking_RealisticConversation tests the importance
// ranking compressor with a realistic multi-turn agent conversation including
// system messages, tool calls, and mixed-role messages.
func TestIntegration_ImportanceRanking_RealisticConversation(t *testing.T) {
	c := memory.NewImportanceRankingCompressorWithDefaults()

	// Test: system messages always preserved under budget pressure.
	t.Run("system messages preserved under pressure", func(t *testing.T) {
		msgs := []schema.Message{
			newSystemMessage("sys"),                             // 1 token, score ~1000
			schema.NewUserMessage("question 1"),                 // ~2 tokens, score ~50
			newAssistantMessage(strings.Repeat("verbose ", 20)), // ~35 tokens, score ~10
			schema.NewUserMessage("question 2"),                 // ~2 tokens, score ~50
			newAssistantMessage("short"),                        // 1 token, score ~10
			schema.NewUserMessage("question 3"),                 // ~2 tokens, score ~50
		}

		// Budget=8 should keep system + all users + short assistant
		result, err := c.Compress(context.Background(), msgs, 8)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// System message must be present
		hasSystem := false
		for _, m := range result {
			if m.Role == aimodel.RoleSystem {
				hasSystem = true
				break
			}
		}
		if !hasSystem {
			t.Error("system message should be retained")
		}

		// Verbose assistant message should be dropped
		for _, m := range result {
			if strings.Contains(m.Content.Text(), "verbose") {
				t.Error("verbose assistant message should have been dropped")
			}
		}
	})

	// Test: tool call messages get higher priority than plain assistant messages.
	t.Run("tool calls prioritized over plain assistant", func(t *testing.T) {
		msgs := []schema.Message{
			newAssistantMessage(strings.Repeat("a", 16)), // 4 tokens, plain assistant, low
			newAssistantWithToolCalls("call"),            // 1 token, assistant+toolcall, high
			newToolMessage("result"),                     // ~1 token, tool, high
			newAssistantMessage(strings.Repeat("b", 16)), // 4 tokens, plain assistant, low
		}

		// Budget=3: should keep tool call assistant(1) + tool result(1) + one more
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should contain the tool call and tool result
		hasToolCall := false
		hasToolResult := false
		for _, m := range result {
			if m.Role == aimodel.RoleAssistant && len(m.ToolCalls) > 0 {
				hasToolCall = true
			}
			if m.Role == aimodel.RoleTool {
				hasToolResult = true
			}
		}
		if !hasToolCall {
			t.Error("assistant message with tool calls should be retained")
		}
		if !hasToolResult {
			t.Error("tool result message should be retained")
		}
	})

	// Test: output preserves chronological order.
	t.Run("output in chronological order", func(t *testing.T) {
		now := time.Now()
		msgs := []schema.Message{
			{Message: aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")}, Timestamp: now},
			{Message: aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("q1")}, Timestamp: now.Add(1 * time.Second)},
			{Message: aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("a1")}, Timestamp: now.Add(2 * time.Second)},
			{Message: aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("q2")}, Timestamp: now.Add(3 * time.Second)},
		}

		result, err := c.Compress(context.Background(), msgs, 4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 1; i < len(result); i++ {
			if result[i].Timestamp.Before(result[i-1].Timestamp) {
				t.Errorf("message %d timestamp (%v) is before message %d (%v)",
					i, result[i].Timestamp, i-1, result[i-1].Timestamp)
			}
		}
	})

	// Test: custom scorer overrides default behavior.
	t.Run("custom scorer reverses priority", func(t *testing.T) {
		// Scorer that prioritizes assistant messages over system
		reverseScorer := func(messages []schema.Message, index int) float64 {
			switch messages[index].Role {
			case aimodel.RoleAssistant:
				return 1000
			case aimodel.RoleSystem:
				return 1
			default:
				return 50
			}
		}

		rc := memory.NewImportanceRankingCompressor(reverseScorer)
		msgs := []schema.Message{
			newSystemMessage("sys"),      // 1 token, score=1
			newAssistantMessage("asst"),  // 1 token, score=1000
			schema.NewUserMessage("usr"), // 1 token, score=50
		}

		// Budget=2: should keep assistant and user (higher scores), drop system
		result, err := rc.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}

		// System message should be dropped
		for _, m := range result {
			if m.Role == aimodel.RoleSystem {
				t.Error("system message should have been dropped with reverse scorer")
			}
		}
	})
}

// =============================================================================
// Integration Test: Concurrent Safety of All Compressors
// =============================================================================

// TestIntegration_ConcurrentSafety verifies that all compressors are safe for
// concurrent use from multiple goroutines, as specified in the design.
func TestIntegration_ConcurrentSafety(t *testing.T) {
	summarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
		return "summary", nil
	}

	compressors := map[string]memory.ContextCompressor{
		"SlidingWindow":     memory.NewSlidingWindowCompressor(5),
		"TokenBudget":       memory.NewTokenBudgetCompressor(),
		"SummarizeAndTrunc": memory.NewSummarizeAndTruncCompressor(summarizer, 2),
		"ImportanceRanking": memory.NewImportanceRankingCompressorWithDefaults(),
	}

	msgs := []schema.Message{
		newSystemMessage("system"),
		schema.NewUserMessage("question 1"),
		newAssistantMessage("answer 1"),
		schema.NewUserMessage("question 2"),
		newAssistantMessage("answer 2"),
	}

	const goroutines = 50

	for name, c := range compressors {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			errs := make(chan error, goroutines)

			for i := range goroutines {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					maxTokens := 3 + (idx % 5) // vary token budget
					result, err := c.Compress(context.Background(), msgs, maxTokens)
					if err != nil {
						errs <- fmt.Errorf("goroutine %d: %w", idx, err)
						return
					}
					if len(result) == 0 {
						errs <- fmt.Errorf("goroutine %d: got empty result", idx)
						return
					}
					errs <- nil
				}(i)
			}

			wg.Wait()
			close(errs)

			for err := range errs {
				if err != nil {
					t.Errorf("concurrent error: %v", err)
				}
			}
		})
	}
}

// =============================================================================
// Integration Test: Context Deadline Exceeded
// =============================================================================

// TestIntegration_ContextDeadlineExceeded verifies that compressors respect
// context deadlines, not just cancellations.
func TestIntegration_ContextDeadlineExceeded(t *testing.T) {
	summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
		return "summary", nil
	}

	compressors := map[string]memory.ContextCompressor{
		"SlidingWindow":     memory.NewSlidingWindowCompressor(5),
		"TokenBudget":       memory.NewTokenBudgetCompressor(),
		"SummarizeAndTrunc": memory.NewSummarizeAndTruncCompressor(summarizer, 2),
		"ImportanceRanking": memory.NewImportanceRankingCompressorWithDefaults(),
	}

	for name, c := range compressors {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
			defer cancel()

			msgs := []schema.Message{schema.NewUserMessage("test")}
			_, err := c.Compress(ctx, msgs, 100)
			if err == nil {
				t.Fatal("expected error for expired deadline")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

// =============================================================================
// Integration Test: Large Message Volume
// =============================================================================

// TestIntegration_LargeMessageVolume tests compressors with a large number
// of messages to verify performance and correctness at scale.
func TestIntegration_LargeMessageVolume(t *testing.T) {
	const messageCount = 1000

	msgs := make([]schema.Message, messageCount)
	for i := range messageCount {
		switch i % 4 {
		case 0:
			msgs[i] = schema.NewUserMessage(fmt.Sprintf("user message %d with content", i))
		case 1:
			msgs[i] = newAssistantMessage(fmt.Sprintf("assistant reply %d", i))
		case 2:
			msgs[i] = newToolMessage(fmt.Sprintf("tool result %d", i))
		case 3:
			msgs[i] = newSystemMessage(fmt.Sprintf("system note %d", i))
		}
	}

	// Test: SlidingWindow handles large volume.
	t.Run("SlidingWindow 1000 messages", func(t *testing.T) {
		c := memory.NewSlidingWindowCompressor(10)
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 10 {
			t.Fatalf("got %d messages, want 10", len(result))
		}
	})

	// Test: TokenBudget handles large volume.
	t.Run("TokenBudget 1000 messages", func(t *testing.T) {
		c := memory.NewTokenBudgetCompressor()
		result, err := c.Compress(context.Background(), msgs, 20)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) == 0 {
			t.Fatal("expected at least one message")
		}
		if len(result) >= messageCount {
			t.Errorf("expected fewer than %d messages, got %d", messageCount, len(result))
		}
	})

	// Test: ImportanceRanking handles large volume.
	t.Run("ImportanceRanking 1000 messages", func(t *testing.T) {
		c := memory.NewImportanceRankingCompressorWithDefaults()
		result, err := c.Compress(context.Background(), msgs, 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) == 0 {
			t.Fatal("expected at least one message")
		}
		// System messages should be preferentially retained
		systemCount := 0
		for _, m := range result {
			if m.Role == aimodel.RoleSystem {
				systemCount++
			}
		}
		if systemCount == 0 {
			t.Error("expected at least one system message retained")
		}
	})
}

// =============================================================================
// Integration Test: Edge Cases Across Compressors
// =============================================================================

// TestIntegration_EdgeCases tests various edge cases that span multiple
// compressors or test boundary conditions.
func TestIntegration_EdgeCases(t *testing.T) {
	// Test: SlidingWindow with maxTokens that exactly fits all windowed messages.
	t.Run("exact token budget fits all windowed messages", func(t *testing.T) {
		c := memory.NewSlidingWindowCompressor(3)
		msgs := []schema.Message{
			schema.NewUserMessage("aaaa"),     // 1 token
			schema.NewUserMessage("bbbb"),     // 1 token
			schema.NewUserMessage("cccccccc"), // 2 tokens
		}
		// Exact budget: 1+1+2=4
		result, err := c.Compress(context.Background(), msgs, 4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	// Test: TokenBudget with all single-character messages (minimum-of-1 token rule).
	t.Run("minimum token estimation single char messages", func(t *testing.T) {
		c := memory.NewTokenBudgetCompressor()
		msgs := []schema.Message{
			schema.NewUserMessage("a"), // 1 token (min-of-1 rule)
			schema.NewUserMessage("b"), // 1 token
			schema.NewUserMessage("c"), // 1 token
		}
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Content.Text() != "b" {
			t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "b")
		}
	})

	// Test: ImportanceRanking with equal scores uses stable sort (preserves order).
	t.Run("stable sort with equal scores", func(t *testing.T) {
		constantScorer := func(_ []schema.Message, _ int) float64 {
			return 42.0
		}
		c := memory.NewImportanceRankingCompressor(constantScorer)
		msgs := []schema.Message{
			schema.NewUserMessage("first"),
			schema.NewUserMessage("second"),
			schema.NewUserMessage("third"),
			schema.NewUserMessage("fourth"),
		}
		// Budget for 2: stable sort means first 2 by original order selected
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		// With stable sort and equal scores, first two should be selected
		if result[0].Content.Text() != "first" {
			t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "first")
		}
		if result[1].Content.Text() != "second" {
			t.Errorf("result[1] = %q, want %q", result[1].Content.Text(), "second")
		}
	})

	// Test: SummarizeAndTrunc with keepLastN equal to message count — no summarization.
	t.Run("SummarizeAndTrunc keepLastN equals message count", func(t *testing.T) {
		called := false
		summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			called = true
			return "should not be called", nil
		}
		c := memory.NewSummarizeAndTruncCompressor(summarizer, 3)
		msgs := []schema.Message{
			schema.NewUserMessage("a"),
			schema.NewUserMessage("b"),
			schema.NewUserMessage("c"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
		if called {
			t.Error("summarizer should not be called when keepLastN >= len(messages)")
		}
	})

	// Test: Empty content messages have 0 tokens.
	t.Run("empty content messages with TokenBudget", func(t *testing.T) {
		c := memory.NewTokenBudgetCompressor()
		msgs := []schema.Message{
			schema.NewUserMessage(""),     // 0 tokens
			schema.NewUserMessage(""),     // 0 tokens
			schema.NewUserMessage("aaaa"), // 1 token
		}
		// Budget=1: all empty messages (0 tokens) + last message (1 token) = 1 token
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	// Test: Constructor panics.
	t.Run("constructor panics", func(t *testing.T) {
		t.Run("SlidingWindow zero window", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected panic")
				}
			}()
			memory.NewSlidingWindowCompressor(0)
		})

		t.Run("ImportanceRanking nil scorer", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected panic")
				}
			}()
			memory.NewImportanceRankingCompressor(nil)
		})

		t.Run("SummarizeAndTrunc nil summarizer", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected panic")
				}
			}()
			memory.NewSummarizeAndTruncCompressor(nil, 5)
		})

		t.Run("SummarizeAndTrunc zero keepLastN", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected panic")
				}
			}()
			summarizer := func(_ context.Context, _ []schema.Message) (string, error) {
				return "", nil
			}
			memory.NewSummarizeAndTruncCompressor(summarizer, 0)
		})
	})
}

// =============================================================================
// Integration Test: Summarizer Context Propagation
// =============================================================================

// TestIntegration_SummarizeAndTrunc_ContextPropagation verifies that the context
// is properly passed to the summarizer function.
func TestIntegration_SummarizeAndTrunc_ContextPropagation(t *testing.T) {
	type ctxKey string

	var receivedVal string
	summarizer := func(ctx context.Context, _ []schema.Message) (string, error) {
		if v, ok := ctx.Value(ctxKey("test_key")).(string); ok {
			receivedVal = v
		}
		return "summary", nil
	}

	c := memory.NewSummarizeAndTruncCompressor(summarizer, 1)
	msgs := []schema.Message{
		schema.NewUserMessage("old"),
		schema.NewUserMessage("new"),
	}

	ctx := context.WithValue(context.Background(), ctxKey("test_key"), "test_value")
	_, err := c.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedVal != "test_value" {
		t.Errorf("summarizer received context value %q, want %q", receivedVal, "test_value")
	}
}

// =============================================================================
// Integration Test: DefaultMessageScorer Scoring Hierarchy
// =============================================================================

// TestIntegration_DefaultMessageScorer_Hierarchy verifies the complete scoring
// hierarchy of the default scorer across all role types.
func TestIntegration_DefaultMessageScorer_Hierarchy(t *testing.T) {
	c := memory.NewImportanceRankingCompressorWithDefaults()

	// Create messages of each type with enough tokens to test individual selection
	msgs := []schema.Message{
		newSystemMessage("sys"),         // score ~1000
		newToolMessage("tool"),          // score ~100
		newAssistantWithToolCalls("tc"), // score ~100
		schema.NewUserMessage("usr"),    // score ~50
		newAssistantMessage("plain"),    // score ~10
	}

	// Budget=1 should keep system message (highest score)
	t.Run("budget for 1 keeps system", func(t *testing.T) {
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1", len(result))
		}
		if result[0].Role != aimodel.RoleSystem {
			t.Errorf("expected system message, got role %q", result[0].Role)
		}
	})

	// Budget=2 should keep system + one of tool/toolcall (both score ~100)
	t.Run("budget for 2 keeps system plus tool-tier", func(t *testing.T) {
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Role != aimodel.RoleSystem {
			t.Errorf("expected system as first, got %q", result[0].Role)
		}
	})
}
