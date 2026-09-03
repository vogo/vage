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

const defaultTargetUtilization = 0.8

// OverBudgetOption configures SummarizeWhenOverBudget.
type OverBudgetOption func(*OverBudgetSummarizer)

// KeepRecentTurns keeps the last n complete user-bounded turns verbatim
// when history exceeds the budget. n must be positive.
func KeepRecentTurns(n int) OverBudgetOption {
	return func(c *OverBudgetSummarizer) { c.keepTurns = n }
}

// TargetUtilization sets the post-summary occupancy target as a fraction of
// AvailableHistory. Default is 0.8. Must be in (0, 1].
func TargetUtilization(u float64) OverBudgetOption {
	return func(c *OverBudgetSummarizer) { c.utilization = u }
}

// OverBudgetSummarizer summarizes older turns when history exceeds
// AvailableHistory, keeping the most recent complete turns verbatim.
type OverBudgetSummarizer struct {
	summarizer  Summarizer
	keepTurns   int
	utilization float64
	summaryRole schema.Role
}

// SummarizeWhenOverBudget builds a compressor that summarizes older history
// only when the bounded budget is exceeded. Panics on a nil summarizer,
// non-positive KeepRecentTurns, or a utilization outside (0, 1].
func SummarizeWhenOverBudget(summarizer Summarizer, opts ...OverBudgetOption) *OverBudgetSummarizer {
	if summarizer == nil {
		panic("memory: summarizer must not be nil")
	}

	c := &OverBudgetSummarizer{
		summarizer:  summarizer,
		utilization: defaultTargetUtilization,
		summaryRole: schema.RoleUser,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.keepTurns <= 0 {
		panic("memory: KeepRecentTurns must be positive")
	}
	if c.utilization <= 0 || c.utilization > 1 {
		panic("memory: TargetUtilization must be in (0, 1]")
	}

	return c
}

// Compress implements ContextCompressor. maxTokens<=0 is unlimited; a
// positive value is treated as both the window and AvailableHistory.
func (c *OverBudgetSummarizer) Compress(ctx context.Context, messages []schema.Message, maxTokens int) ([]schema.Message, error) {
	in := CompressionInput{Messages: messages}
	if maxTokens > 0 {
		in.Budget.ModelContextTokens = maxTokens
		in.Budget.AvailableHistory = maxTokens
	}
	return c.CompressWithBudget(ctx, in)
}

// CompressWithBudget implements BudgetCompressor.
func (c *OverBudgetSummarizer) CompressWithBudget(ctx context.Context, in CompressionInput) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if in.Budget.Unlimited() {
		return in.Messages, nil
	}
	if in.Budget.BoundedZeroHistory() {
		return []schema.Message{}, nil
	}

	est := in.Budget.EstimatorOrDefault()
	if EstimateMessages(est, in.Messages) <= in.Budget.AvailableHistory {
		return in.Messages, nil
	}

	older, recent := splitRecentTurns(in.Messages, c.keepTurns)
	if len(older) == 0 {
		return in.Messages, nil
	}

	target := int(float64(in.Budget.AvailableHistory) * c.utilization)
	recentTokens := EstimateMessages(est, recent)
	if recentTokens >= target {
		// Recent turns already occupy the target; do not split a turn to
		// make room. The Builder hard-caps the merged prompt later.
		return recent, nil
	}

	summaryText, err := c.summarizer(ctx, older)
	if err != nil {
		return nil, err
	}
	if summaryText == "" {
		return recent, nil
	}

	summaryBudget := target - recentTokens
	tmpMsg := schema.NewTextMessage(schema.ProtocolOf(in.Messages), c.summaryRole, summaryText)
	if est(tmpMsg) > summaryBudget {
		ratio := float64(summaryBudget) / float64(est(tmpMsg))
		maxLen := int(float64(len(summaryText)) * ratio)
		if maxLen <= 0 {
			return recent, nil
		}
		summaryText = summaryText[:maxLen]
	}

	summaryMsg := schema.NewTextMessage(schema.ProtocolOf(in.Messages), c.summaryRole, summaryText)
	summaryMsg.Metadata = map[string]any{
		"compressed":   true,
		"source_count": len(older),
		"strategy":     "summarize_when_over_budget",
	}

	out := make([]schema.Message, 0, 1+len(recent))
	out = append(out, summaryMsg)
	out = append(out, recent...)
	return out, nil
}

// splitRecentTurns keeps the last n complete turns, where a turn starts at
// a user message and includes following assistant/tool messages. Leading
// non-user messages belong to the first (possibly incomplete) turn.
func splitRecentTurns(msgs []schema.Message, n int) (older, recent []schema.Message) {
	if n <= 0 || len(msgs) == 0 {
		return nil, msgs
	}

	userIdx := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if m.Role() == schema.RoleUser {
			userIdx = append(userIdx, i)
		}
	}
	if len(userIdx) == 0 || len(userIdx) <= n {
		return nil, msgs
	}

	split := userIdx[len(userIdx)-n]
	return msgs[:split], msgs[split:]
}
