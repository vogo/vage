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
	"encoding/json"
	"testing"

	"github.com/vogo/vage/schema"
)

// TestBuildReport_ToEventData verifies the in-process report and the
// event payload share scalar fields one-for-one and reuse the same
// per-source slice (no copy).
func TestBuildReport_ToEventData(t *testing.T) {
	r := BuildReport{
		BuilderName:  "x",
		Strategy:     StrategyOrderedGreedy,
		InputBudget:  100,
		OutputCount:  3,
		OutputTokens: 50,
		DroppedCount: 1,
		Sources: []schema.ContextSourceReport{
			{Source: "a", Status: StatusOK, OutputN: 1, Tokens: 10},
		},
		Duration: 7,
	}

	data := r.ToEventData()
	if data.Builder != r.BuilderName {
		t.Errorf("Builder mismatch: %q vs %q", data.Builder, r.BuilderName)
	}
	if data.Strategy != r.Strategy {
		t.Errorf("Strategy mismatch")
	}
	if data.BudgetTotal != r.InputBudget {
		t.Errorf("BudgetTotal mismatch: %d vs %d", data.BudgetTotal, r.InputBudget)
	}
	if data.OutputCount != r.OutputCount {
		t.Errorf("OutputCount mismatch")
	}
	if data.DroppedCount != r.DroppedCount {
		t.Errorf("DroppedCount mismatch")
	}
	if len(data.Sources) != 1 || data.Sources[0].Source != "a" {
		t.Errorf("Sources not propagated: %#v", data.Sources)
	}
}

// TestBuildReport_JSON verifies a populated BuildReport serialises without
// errors and round-trips back into the same structural fields.
func TestBuildReport_JSON(t *testing.T) {
	r := BuildReport{
		BuilderName: "default",
		Strategy:    StrategyOrderedGreedy,
		Sources: []schema.ContextSourceReport{
			{Source: "a", Status: StatusOK, OutputN: 1, Tokens: 4},
			{Source: "b", Status: StatusError, Error: "boom"},
		},
	}

	bytes, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got BuildReport
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.BuilderName != r.BuilderName {
		t.Errorf("BuilderName roundtrip: %q vs %q", got.BuilderName, r.BuilderName)
	}
	if len(got.Sources) != 2 {
		t.Errorf("Sources roundtrip len = %d, want 2", len(got.Sources))
	}
	if got.Sources[1].Error != "boom" {
		t.Errorf("Sources[1].Error roundtrip: %q", got.Sources[1].Error)
	}
}
