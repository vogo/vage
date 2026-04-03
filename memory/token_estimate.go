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
	"github.com/vogo/vage/schema"
)

// TokenEstimator estimates the token count for a message.
// Implementations can use simple heuristics or real tokenizers depending on accuracy needs.
type TokenEstimator func(msg schema.Message) int

// EstimateTextTokens returns the estimated token count for a plain text string.
// Uses the same heuristic as DefaultTokenEstimator: len(text) / 4.
func EstimateTextTokens(text string) int {
	if len(text) == 0 {
		return 0
	}

	tokens := len(text) / 4
	if tokens == 0 {
		tokens = 1
	}

	return tokens
}

// DefaultTokenEstimator returns an approximate token count for a message.
// Uses a simple heuristic: len(text) / 4, with a minimum of 1 for non-empty content.
//
// The minimum-of-1 rule prevents zero-token messages from bypassing budget checks.
// This means messages with 1-3 characters all estimate as 1 token, which slightly
// overcounts short messages. This is acceptable for a rough heuristic.
//
// Known limitation: only the text portion of Content is considered. Multimodal
// content parts (images, etc.) are not accounted for in the estimate.
func DefaultTokenEstimator(msg schema.Message) int {
	text := msg.Content.Text()
	if len(text) == 0 {
		return 0
	}

	tokens := len(text) / 4
	if tokens == 0 {
		tokens = 1
	}

	return tokens
}
