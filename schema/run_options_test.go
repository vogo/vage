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

import (
	"encoding/json"
	"testing"
)

func TestRunOptions_OldPayloadRoundTrip(t *testing.T) {
	raw := []byte(`{"model":"gpt-x","max_iterations":3,"max_tokens":100,"run_token_budget":50,"tools":["a"]}`)

	var opts RunOptions
	if err := json.Unmarshal(raw, &opts); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if opts.Model != "gpt-x" || opts.MaxIterations != 3 || opts.MaxTokens != 100 || opts.RunTokenBudget != 50 {
		t.Errorf("old fields = %+v", opts)
	}
	if opts.Limits != nil || opts.ToolMode != "" {
		t.Errorf("new fields should stay unset on old payload: limits=%v tool_mode=%q", opts.Limits, opts.ToolMode)
	}

	out, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var again RunOptions
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatalf("re-Unmarshal: %v", err)
	}
	if again.Model != opts.Model || again.MaxIterations != opts.MaxIterations {
		t.Errorf("round trip drifted: %+v", again)
	}
}

func TestRunOptions_LimitsAndToolModeRoundTrip(t *testing.T) {
	modes := []string{"", ToolModeNone, ToolModeAllow, ToolModeAll}

	zero, four := 0, 4
	for _, mode := range modes {
		opts := RunOptions{
			Model:    "m",
			ToolMode: mode,
			Tools:    []string{"t1"},
			Limits: &RunLimits{
				MaxIterations:  &four,
				MaxTokens:      &zero,
				RunTokenBudget: &zero,
			},
		}
		raw, err := json.Marshal(opts)
		if err != nil {
			t.Fatalf("Marshal mode %q: %v", mode, err)
		}
		var got RunOptions
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("Unmarshal mode %q: %v", mode, err)
		}
		if got.ToolMode != mode {
			t.Errorf("tool_mode = %q, want %q", got.ToolMode, mode)
		}
		if got.Limits == nil || got.Limits.MaxTokens == nil || *got.Limits.MaxTokens != 0 {
			t.Errorf("Limits.MaxTokens ptr(0) not preserved for mode %q: %+v", mode, got.Limits)
		}
		if got.Limits.RunTokenBudget == nil || *got.Limits.RunTokenBudget != 0 {
			t.Errorf("Limits.RunTokenBudget ptr(0) not preserved for mode %q", mode)
		}
		if got.Limits.MaxIterations == nil || *got.Limits.MaxIterations != 4 {
			t.Errorf("Limits.MaxIterations not preserved for mode %q", mode)
		}
	}
}

func TestRunOptions_LimitsZeroIsPresent(t *testing.T) {
	raw := []byte(`{"limits":{"run_token_budget":0,"max_tokens":0,"max_iterations":2}}`)

	var opts RunOptions
	if err := json.Unmarshal(raw, &opts); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if opts.Limits == nil {
		t.Fatal("Limits is nil")
	}
	if opts.Limits.RunTokenBudget == nil || *opts.Limits.RunTokenBudget != 0 {
		t.Error("RunTokenBudget ptr(0) missing")
	}
	if opts.Limits.MaxTokens == nil || *opts.Limits.MaxTokens != 0 {
		t.Error("MaxTokens ptr(0) missing")
	}
	if opts.RunTokenBudget != 0 || opts.MaxTokens != 0 {
		t.Error("old int fields must stay zero when only Limits is set")
	}
}
