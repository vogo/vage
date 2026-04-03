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
	"fmt"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// ConversationCompactor compresses a conversation history by summarizing
// older messages while preserving protected messages (system prompt and
// recent turn pairs).
//
// ConversationCompactor is safe for concurrent use; it holds no mutable state.
type ConversationCompactor struct {
	summarizer     Summarizer
	estimator      TokenEstimator
	protectedTurns int // number of recent user/assistant turn pairs to protect
	maxInputTokens int // max tokens for summarizer input; 0 means no limit
}

// NewConversationCompactor creates a ConversationCompactor.
// protectedTurns is the number of recent turn pairs (user + assistant) to keep verbatim.
func NewConversationCompactor(summarizer Summarizer, protectedTurns int) *ConversationCompactor {
	if protectedTurns < 0 {
		protectedTurns = 0
	}

	return &ConversationCompactor{
		summarizer:     summarizer,
		estimator:      DefaultTokenEstimator,
		protectedTurns: protectedTurns,
	}
}

// WithTokenEstimator sets a custom token estimator.
func (c *ConversationCompactor) WithTokenEstimator(est TokenEstimator) *ConversationCompactor {
	if est != nil {
		c.estimator = est
	}

	return c
}

// WithMaxInputTokens sets the maximum token count for the summarizer input.
// If the eligible messages exceed this, the input is truncated with an omission marker.
// This prevents the summarization call itself from exceeding the context window.
func (c *ConversationCompactor) WithMaxInputTokens(n int) *ConversationCompactor {
	c.maxInputTokens = n
	return c
}

// Summarizer returns the underlying summarizer function.
func (c *ConversationCompactor) Summarizer() Summarizer {
	return c.summarizer
}

// EstimateTokens returns the total estimated token count for a message slice.
func (c *ConversationCompactor) EstimateTokens(messages []schema.Message) int {
	total := 0
	for _, msg := range messages {
		total += c.estimator(msg)
	}

	return total
}

// Compact compresses messages by summarizing eligible (non-protected) messages.
// Returns the compressed message slice and the estimated token count of the result.
//
// Protected messages: the first message if it is a system message, plus the last
// protectedTurns user/assistant exchange pairs.
//
// The summary replaces all eligible messages with a single system-role message
// carrying metadata {"compressed": true, "source_count": N, "strategy": "conversation_compact"}.
func (c *ConversationCompactor) Compact(ctx context.Context, messages []schema.Message) ([]schema.Message, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	if len(messages) == 0 {
		return messages, 0, nil
	}

	// Identify system prompt.
	systemIdx := -1
	if messages[0].Role == aimodel.RoleSystem {
		systemIdx = 0
	}

	// Walk backward to find protected tail (last protectedTurns user/assistant pairs).
	protectedStart := len(messages)
	pairsFound := 0

	for i := len(messages) - 1; i >= 0 && pairsFound < c.protectedTurns; i-- {
		// Skip system prompt.
		if i == systemIdx {
			break
		}

		if messages[i].Role == aimodel.RoleAssistant {
			// Look for the preceding user message.
			if i > 0 && messages[i-1].Role == aimodel.RoleUser && i-1 != systemIdx {
				protectedStart = i - 1
				pairsFound++
				i-- // skip the user message we just consumed
			} else {
				// Standalone assistant message; protect it as part of a partial pair.
				protectedStart = i
			}
		}
	}

	// Determine eligible range.
	eligibleStart := 0
	if systemIdx == 0 {
		eligibleStart = 1
	}

	eligibleEnd := protectedStart

	// If no eligible messages, return as-is.
	if eligibleStart >= eligibleEnd {
		return messages, c.EstimateTokens(messages), nil
	}

	eligible := messages[eligibleStart:eligibleEnd]

	// Optionally truncate summarizer input.
	summarizerInput := eligible
	if c.maxInputTokens > 0 {
		inputTokens := c.EstimateTokens(eligible)
		if inputTokens > c.maxInputTokens {
			summarizerInput = c.truncateInput(eligible)
		}
	}

	// Call summarizer.
	summaryText, err := c.summarizer(ctx, summarizerInput)
	if err != nil {
		return nil, 0, fmt.Errorf("compactor summarize: %w", err)
	}

	// Build summary message.
	summaryMsg := schema.Message{
		Message:   aimodel.Message{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent(summaryText)},
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"compressed":   true,
			"source_count": len(eligible),
			"strategy":     "conversation_compact",
		},
	}

	// Reassemble: [systemPrompt] + [summaryMsg] + [protectedTail].
	var result []schema.Message
	if systemIdx == 0 {
		result = append(result, messages[0])
	}

	result = append(result, summaryMsg)
	result = append(result, messages[protectedStart:]...)

	return result, c.EstimateTokens(result), nil
}

// truncateInput keeps the first and last portions of eligible messages,
// inserting an omission marker in the middle.
func (c *ConversationCompactor) truncateInput(eligible []schema.Message) []schema.Message {
	if len(eligible) <= 2 {
		return eligible
	}

	// Keep first third and last third.
	keepCount := len(eligible) / 3
	if keepCount == 0 {
		keepCount = 1
	}

	omitted := len(eligible) - 2*keepCount
	marker := schema.Message{
		Message: aimodel.Message{
			Role: aimodel.RoleSystem,
			Content: aimodel.NewTextContent(
				fmt.Sprintf("[... %d messages omitted ...]", omitted),
			),
		},
		Timestamp: time.Now(),
	}

	result := make([]schema.Message, 0, 2*keepCount+1)
	result = append(result, eligible[:keepCount]...)
	result = append(result, marker)
	result = append(result, eligible[len(eligible)-keepCount:]...)

	return result
}

// CompactIfNeeded checks the estimated token count and compresses if above the threshold.
// Returns the (possibly compressed) messages, updated token estimate, whether compaction
// occurred, and any error.
// This is a convenience for callers (CLI, HTTP handler) to avoid duplicating the check logic.
func CompactIfNeeded(
	ctx context.Context,
	compactor *ConversationCompactor,
	messages []schema.Message,
	threshold int,
) ([]schema.Message, int, bool, error) {
	if compactor == nil {
		return messages, 0, false, nil
	}

	tokens := compactor.EstimateTokens(messages)
	if tokens <= threshold {
		return messages, tokens, false, nil
	}

	compressed, newTokens, err := compactor.Compact(ctx, messages)
	if err != nil {
		return messages, tokens, false, err
	}

	return compressed, newTokens, true, nil
}
