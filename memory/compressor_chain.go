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

// ChainCompressor applies multiple compressors in sequence, passing the output
// of each compressor as input to the next.
type ChainCompressor struct {
	compressors []ContextCompressor
}

// NewChainCompressor creates a ChainCompressor that applies the given compressors in order.
func NewChainCompressor(compressors ...ContextCompressor) *ChainCompressor {
	return &ChainCompressor{compressors: compressors}
}

// Compress applies each compressor in sequence.
func (c *ChainCompressor) Compress(ctx context.Context, messages []schema.Message, maxTokens int) ([]schema.Message, error) {
	result := messages

	for _, comp := range c.compressors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var err error

		result, err = comp.Compress(ctx, result, maxTokens)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

// CompressWithBudget applies each compressor in sequence, preferring a
// budget-aware entry when the child implements BudgetCompressor.
func (c *ChainCompressor) CompressWithBudget(ctx context.Context, in CompressionInput) ([]schema.Message, error) {
	result := in.Messages

	for _, comp := range c.compressors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var err error
		result, err = CompressWithBudget(ctx, comp, CompressionInput{Messages: result, Budget: in.Budget})
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}
