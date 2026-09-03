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
	"sort"

	"github.com/vogo/vage/schema"
)

// MessageScorer assigns an importance score to a message.
// Higher scores indicate more important messages.
// The full message slice is provided so scorers can make context-aware decisions
// (e.g., keeping tool-call/tool-result pairs together).
// Parameters: the full messages slice and the index of the message to score.
type MessageScorer func(messages []schema.Message, index int) float64

// ImportanceRankingCompressor keeps the most important messages within a token budget,
// using a scorer function to rank messages by importance.
// The most important message is always retained regardless of token budget.
type ImportanceRankingCompressor struct {
	scorer    MessageScorer
	estimator TokenEstimator
}

// NewImportanceRankingCompressor creates an ImportanceRankingCompressor with the given scorer.
// Panics if scorer is nil.
func NewImportanceRankingCompressor(scorer MessageScorer) *ImportanceRankingCompressor {
	if scorer == nil {
		panic("memory: scorer must not be nil")
	}

	return &ImportanceRankingCompressor{
		scorer:    scorer,
		estimator: DefaultTokenEstimator,
	}
}

// NewImportanceRankingCompressorWithDefaults creates a compressor using DefaultMessageScorer.
func NewImportanceRankingCompressorWithDefaults() *ImportanceRankingCompressor {
	return NewImportanceRankingCompressor(DefaultMessageScorer)
}

// WithTokenEstimator sets a custom token estimator. Nil values are ignored.
func (c *ImportanceRankingCompressor) WithTokenEstimator(est TokenEstimator) *ImportanceRankingCompressor {
	if est != nil {
		c.estimator = est
	}

	return c
}

// DefaultMessageScorer provides a heuristic scoring function.
// System messages score highest (1000), followed by tool messages and assistant
// messages with tool calls (100), user messages (50), and plain assistant messages (10).
// A proportional recency bonus of up to 5% of the base score is added,
// so recency matters within the same role but does not override role priority.
func DefaultMessageScorer(messages []schema.Message, index int) float64 {
	msg := messages[index]
	total := len(messages)

	var base float64

	switch msg.Role() {
	case schema.RoleSystem:
		base = 1000
	case schema.RoleTool:
		base = 100
	case schema.RoleAssistant:
		if len(msg.ToolCalls()) > 0 {
			base = 100
		} else {
			base = 10
		}
	case schema.RoleUser:
		base = 50
	default:
		base = 10
	}

	recencyBonus := base * 0.05 * float64(index) / float64(total)

	return base + recencyBonus
}

type scoredIndex struct {
	index int
	score float64
}

// Compress keeps the most important messages that fit within maxTokens.
// If maxTokens is 0 or negative, all messages are returned unchanged.
// The highest-scored message is always retained regardless of budget.
// Output messages are in chronological order.
func (c *ImportanceRankingCompressor) Compress(ctx context.Context, messages []schema.Message, maxTokens int) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(messages) == 0 || maxTokens <= 0 {
		return messages, nil
	}

	total := len(messages)
	scored := make([]scoredIndex, total)

	for i := range messages {
		scored[i] = scoredIndex{
			index: i,
			score: c.scorer(messages, i),
		}
	}

	// Sort by score descending; stable sort preserves original order for equal scores.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Greedily select messages by score order, accumulating tokens.
	// The first message (highest score) is always included regardless of budget.
	tokenTotal := 0
	selected := make([]int, 0, total)

	for _, si := range scored {
		tokens := c.estimator(messages[si.index])
		if len(selected) > 0 && tokenTotal+tokens > maxTokens {
			continue
		}

		tokenTotal += tokens
		selected = append(selected, si.index)
	}

	// Sort selected indices to restore chronological order.
	sort.Ints(selected)

	result := make([]schema.Message, len(selected))
	for i, idx := range selected {
		result[i] = messages[idx]
	}

	return result, nil
}

// CompressWithBudget ranks by importance within AvailableHistory.
// Bounded zero remaining returns empty history.
func (c *ImportanceRankingCompressor) CompressWithBudget(ctx context.Context, in CompressionInput) ([]schema.Message, error) {
	return budgetAwareCompress(ctx, in, c.Compress)
}
