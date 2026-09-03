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

	"github.com/vogo/vage/schema"
)

// Summarizer is a function that summarizes messages into a single text string.
type Summarizer func(ctx context.Context, messages []schema.Message) (string, error)

// SummarizeAndTruncOption configures a SummarizeAndTruncCompressor.
type SummarizeAndTruncOption func(*SummarizeAndTruncCompressor)

// WithSummaryRole sets the role assigned to the summary message.
// Defaults to schema.RoleUser.
func WithSummaryRole(role schema.Role) SummarizeAndTruncOption {
	return func(c *SummarizeAndTruncCompressor) {
		c.summaryRole = role
	}
}

// SummarizeAndTruncCompressor splits messages into older and recent parts,
// summarizes the older messages, and prepends the summary to the recent ones.
// The keepLastN parameter controls how many recent messages to keep verbatim.
// When maxTokens > 0, the summary text is truncated to fit the remaining token budget
// after accounting for the recent messages.
type SummarizeAndTruncCompressor struct {
	summarizer  Summarizer
	keepLastN   int
	summaryRole schema.Role
	estimator   TokenEstimator
}

// NewSummarizeAndTruncCompressor creates a new SummarizeAndTruncCompressor.
// Panics if summarizer is nil or keepLastN <= 0.
func NewSummarizeAndTruncCompressor(summarizer Summarizer, keepLastN int, opts ...SummarizeAndTruncOption) *SummarizeAndTruncCompressor {
	if summarizer == nil {
		panic("memory: summarizer must not be nil")
	}

	if keepLastN <= 0 {
		panic("memory: keepLastN must be positive")
	}

	c := &SummarizeAndTruncCompressor{
		summarizer:  summarizer,
		keepLastN:   keepLastN,
		summaryRole: schema.RoleUser,
		estimator:   DefaultTokenEstimator,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// WithTokenEstimator sets a custom token estimator. Nil values are ignored.
func (c *SummarizeAndTruncCompressor) WithTokenEstimator(est TokenEstimator) *SummarizeAndTruncCompressor {
	if est != nil {
		c.estimator = est
	}

	return c
}

// Compress summarizes older messages and keeps the last keepLastN messages verbatim.
// When maxTokens > 0, the summary text is truncated proportionally to fit the
// remaining token budget after accounting for recent messages.
func (c *SummarizeAndTruncCompressor) Compress(ctx context.Context, messages []schema.Message, maxTokens int) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(messages) <= c.keepLastN {
		return messages, nil
	}

	splitIdx := len(messages) - c.keepLastN
	older := messages[:splitIdx]
	recent := messages[splitIdx:]

	summaryText, err := c.summarizer(ctx, older)
	if err != nil {
		return nil, err
	}

	// If summary text is empty, return recent only (skip creating an empty summary message).
	if summaryText == "" {
		return recent, nil
	}

	// Respect token budget if specified.
	if maxTokens > 0 {
		recentTokens := 0
		for _, m := range recent {
			recentTokens += c.estimator(m)
		}

		summaryBudget := maxTokens - recentTokens
		if summaryBudget <= 0 {
			// No room for summary, return recent only.
			return recent, nil
		}

		// Check if summary exceeds remaining budget; truncate proportionally if necessary.
		tmpMsg := schema.NewTextMessage(schema.ProtocolOf(messages), c.summaryRole, summaryText)
		summaryTokens := c.estimator(tmpMsg)

		if summaryTokens > summaryBudget {
			ratio := float64(summaryBudget) / float64(summaryTokens)
			maxLen := int(float64(len(summaryText)) * ratio)

			if maxLen > 0 {
				summaryText = summaryText[:maxLen]
			} else {
				return recent, nil
			}
		}
	}

	summaryMsg := schema.NewTextMessage(schema.ProtocolOf(messages), c.summaryRole, summaryText)
	summaryMsg.Metadata = map[string]any{
		"compressed":   true,
		"source_count": len(older),
		"strategy":     "summarize_and_trunc",
	}

	result := make([]schema.Message, 0, 1+len(recent))
	result = append(result, summaryMsg)
	result = append(result, recent...)

	return result, nil
}

// CompressWithBudget summarizes older messages within AvailableHistory.
// Bounded zero remaining returns empty history without calling the summarizer.
func (c *SummarizeAndTruncCompressor) CompressWithBudget(ctx context.Context, in CompressionInput) ([]schema.Message, error) {
	return budgetAwareCompress(ctx, in, c.Compress)
}
