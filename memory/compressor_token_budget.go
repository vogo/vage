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
	"slices"

	"github.com/vogo/vage/schema"
)

// TokenBudgetCompressor keeps the most recent messages that fit within a token budget.
// It uses a simple reverse-iteration strategy: starting from the newest message,
// it accumulates messages until the budget is exhausted.
type TokenBudgetCompressor struct {
	estimator TokenEstimator
}

// NewTokenBudgetCompressor creates a new TokenBudgetCompressor with DefaultTokenEstimator.
func NewTokenBudgetCompressor() *TokenBudgetCompressor {
	return &TokenBudgetCompressor{estimator: DefaultTokenEstimator}
}

// WithTokenEstimator sets a custom token estimator. Nil values are ignored.
func (c *TokenBudgetCompressor) WithTokenEstimator(est TokenEstimator) *TokenBudgetCompressor {
	if est != nil {
		c.estimator = est
	}

	return c
}

// Compress keeps the most recent messages that fit within maxTokens.
// If maxTokens is 0 or negative, all messages are returned unchanged.
// At least one message (the most recent) is always returned for non-empty input.
func (c *TokenBudgetCompressor) Compress(ctx context.Context, messages []schema.Message, maxTokens int) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(messages) == 0 || maxTokens <= 0 {
		return messages, nil
	}

	total := 0
	startIdx := len(messages)

	for i, message := range slices.Backward(messages) {
		tokens := c.estimator(message)
		if total+tokens > maxTokens {
			break
		}

		total += tokens
		startIdx = i
	}

	// Always include at least the last message.
	if startIdx >= len(messages) {
		startIdx = len(messages) - 1
	}

	return messages[startIdx:], nil
}

// CompressWithBudget keeps the most recent messages that fit AvailableHistory.
// Bounded zero remaining returns empty history instead of the legacy
// "always keep one" behaviour of Compress(..., maxTokens>0).
func (c *TokenBudgetCompressor) CompressWithBudget(ctx context.Context, in CompressionInput) ([]schema.Message, error) {
	return budgetAwareCompress(ctx, in, c.Compress)
}
