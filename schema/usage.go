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

package schema

// Usage reports token consumption for one or more model calls. The vendors
// report tokens under different names and nest the details differently, so
// vage normalizes them into this one accounting type at the protocol boundary
// — token budgets, metrics and session aggregation all read these fields
// regardless of which vendor served the call.
//
// The JSON field names are kept stable across the aimodel migration so
// persisted sessions and downstream consumers keep parsing.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// CacheReadTokens counts prompt tokens served from the vendor's prompt
	// cache. It is a subset of PromptTokens, surfaced separately because it
	// is priced differently. Mapped from OpenAI's
	// prompt_tokens_details.cached_tokens and Anthropic's
	// cache_read_input_tokens.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`

	// CacheWriteTokens counts prompt tokens written into the vendor's prompt
	// cache. Anthropic reports it as cache_creation_input_tokens; OpenAI does
	// not report it and leaves this zero.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`

	// ReasoningTokens counts tokens spent on a reasoning model's internal
	// thinking. It is a subset of CompletionTokens. Mapped from OpenAI's
	// completion_tokens_details.reasoning_tokens and Anthropic's
	// output_tokens_details.reasoning_tokens.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	// ServiceTier names the latency/throughput tier that served the request.
	// It describes a single call and is therefore not combined by Add.
	ServiceTier string `json:"service_tier,omitempty"`
}

// Add accumulates another Usage into this one. Token counts are summed;
// ServiceTier describes one request and is left untouched.
//
// Add tolerates a nil argument so callers can accumulate an optional usage
// (a stream that never reported one, say) without a nil check at every site.
func (u *Usage) Add(other *Usage) {
	if other == nil {
		return
	}

	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.ReasoningTokens += other.ReasoningTokens
}
