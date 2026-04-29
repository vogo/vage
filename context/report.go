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

package vctx

import (
	"github.com/vogo/vage/schema"
)

// BuildReport is the audit payload emitted by a Builder.Build call. The
// per-source list reuses schema.ContextSourceReport so the same value can
// serve both the in-process API and the EventContextBuilt wire format.
type BuildReport struct {
	BuilderName  string                       `json:"builder"`
	Strategy     string                       `json:"strategy"`
	InputBudget  int                          `json:"input_budget"`
	OutputCount  int                          `json:"output_count"`
	OutputTokens int                          `json:"output_tokens"`
	DroppedCount int                          `json:"dropped_count"`
	Sources      []schema.ContextSourceReport `json:"sources"`
	Duration     int64                        `json:"duration_ms"`
}

// ToEventData converts a BuildReport into the EventContextBuilt payload.
// The two structures share schema.ContextSourceReport so the source list
// is reused without copying.
func (r BuildReport) ToEventData() schema.ContextBuiltData {
	return schema.ContextBuiltData{
		Builder:      r.BuilderName,
		Strategy:     r.Strategy,
		BudgetTotal:  r.InputBudget,
		OutputCount:  r.OutputCount,
		OutputTokens: r.OutputTokens,
		DroppedCount: r.DroppedCount,
		Sources:      r.Sources,
		Duration:     r.Duration,
	}
}
