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

import "testing"

// TestRunResponse_IsInterrupted pins the deliberately defensive rule: either
// signal alone rejects, so an Agent or middleware that produces an
// inconsistent pair cannot slip a suspended response past a parent layer.
func TestRunResponse_IsInterrupted(t *testing.T) {
	cases := []struct {
		name string
		resp *RunResponse
		want bool
	}{
		{"both signals", &RunResponse{
			StopReason: StopReasonInterrupted,
			Interrupt:  &InterruptDescriptor{InterruptID: "int-1"},
		}, true},
		{"stop reason only", &RunResponse{StopReason: StopReasonInterrupted}, true},
		{"descriptor only", &RunResponse{Interrupt: &InterruptDescriptor{InterruptID: "int-1"}}, true},
		{"complete", &RunResponse{StopReason: StopReasonComplete}, false},
		{"max iterations", &RunResponse{StopReason: StopReasonMaxIterations}, false},
		{"budget exhausted", &RunResponse{StopReason: StopReasonBudgetExhausted}, false},
		{"zero value", &RunResponse{}, false},
		{"nil receiver", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resp.IsInterrupted(); got != tc.want {
				t.Fatalf("IsInterrupted() = %v, want %v", got, tc.want)
			}
		})
	}
}
