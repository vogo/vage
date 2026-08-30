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

package agenttool_tests

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/integrations/internal/subagent"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/tool/agenttool"
)

// TestAgentAsTool_SuspendedSubAgent_ReturnsErrorResult covers the agent-as-tool
// half of the parent-layer boundary: a real sub-TaskAgent that freezes a tool
// batch for a human decision must reach the parent model as an error result
// saying nested human-in-the-loop is unsupported — never as a successful tool
// result wrapping its half-written turn, and never leaking the interrupt id.
func TestAgentAsTool_SuspendedSubAgent_ReturnsErrorResult(t *testing.T) {
	var handlerRuns atomic.Int32

	reg := tool.NewRegistry()
	if err := agenttool.Register(reg, subagent.SessionScoped(subagent.Suspending("hitl-agent", &handlerRuns), "sess-hitl")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	result, err := reg.Execute(context.Background(), "hitl-agent", `{"input":"do it"}`)
	// Registry contract is unchanged: sub-agent problems surface as an error
	// result, not as a Go error that aborts the parent's tool loop.
	if err != nil {
		t.Fatalf("Execute returned a Go error, want an error ToolResult: %v", err)
	}

	if !result.IsError {
		t.Fatalf("IsError = false, want true (result: %+v)", result)
	}

	text := result.Text()
	for _, want := range []string{"suspended", "nested human-in-the-loop is not supported"} {
		if !strings.Contains(text, want) {
			t.Errorf("result text %q does not mention %q", text, want)
		}
	}

	// The half-written assistant turn and the interrupt id must not travel to
	// the parent model: it cannot act on either, and echoing them is exactly
	// the silent-success failure this boundary exists to prevent.
	if strings.Contains(text, subagent.HalfWrittenText) {
		t.Errorf("result text leaked the sub-agent's half-written turn: %q", text)
	}
	if strings.Contains(text, "interrupt_id") || strings.Contains(text, "tc-1") {
		t.Errorf("result text leaked interrupt metadata: %q", text)
	}

	if handlerRuns.Load() != 0 {
		t.Errorf("flagged tool handler ran %d times, want 0", handlerRuns.Load())
	}
}

// TestAgentAsTool_CompletingSubAgent_StillSucceeds is the control: rejecting
// suspensions must not change the ordinary agent-as-tool path.
func TestAgentAsTool_CompletingSubAgent_StillSucceeds(t *testing.T) {
	reg := tool.NewRegistry()
	if err := agenttool.Register(reg, subagent.Completing("plain-agent", "all done")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	result, err := reg.Execute(context.Background(), "plain-agent", `{"input":"do it"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("IsError = true, want false (result: %+v)", result)
	}
	if !strings.Contains(result.Text(), "all done") {
		t.Errorf("result text = %q, want it to contain %q", result.Text(), "all done")
	}
}
