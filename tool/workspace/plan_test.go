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

package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/workspace"
)

func newTempPlanTool(t *testing.T) (*PlanTool, workspace.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.NewFileWorkspace(root)
	if err != nil {
		t.Fatalf("NewFileWorkspace: %v", err)
	}
	return NewPlanTool(ws), ws, root
}

func contextWith(sessionID string, em schema.Emitter) context.Context {
	ctx := context.Background()
	if sessionID != "" {
		ctx = schema.WithSessionID(ctx, sessionID)
	}
	if em != nil {
		ctx = schema.WithEmitter(ctx, em)
	}
	return ctx
}

// TestPlanTool_Handler covers the canonical write → read-back loop, plus the
// missing-sessionID and bad-arguments error paths.
func TestPlanTool_Handler(t *testing.T) {
	pt, ws, _ := newTempPlanTool(t)

	var captured []schema.Event
	em := schema.Emitter(func(e schema.Event) error {
		captured = append(captured, e)
		return nil
	})

	// Missing session id is an error result (not a returned error).
	res, err := pt.Handler()(context.Background(), "", `{"content":"x"}`)
	if err != nil || !res.IsError {
		t.Fatalf("missing sid: res=%v err=%v, want IsError=true", res, err)
	}

	// Invalid JSON args.
	res, err = pt.Handler()(contextWith("sess", em), "", `not json`)
	if err != nil || !res.IsError {
		t.Fatalf("bad args: res=%v err=%v, want IsError=true", res, err)
	}

	// Happy path: write → emit event → file present.
	captured = nil
	res, err = pt.Handler()(contextWith("sess", em), "", `{"content":"# plan\n- step 1"}`)
	if err != nil || res.IsError {
		t.Fatalf("happy path: err=%v isErr=%v", err, res.IsError)
	}
	got, _ := ws.ReadPlan(context.Background(), "sess")
	if got != "# plan\n- step 1" {
		t.Errorf("ReadPlan = %q", got)
	}
	if len(captured) != 1 {
		t.Fatalf("captured events = %d, want 1", len(captured))
	}
	d, ok := captured[0].Data.(schema.WorkspacePlanUpdatedData)
	if !ok {
		t.Fatalf("event data = %T, want WorkspacePlanUpdatedData", captured[0].Data)
	}
	if d.SessionID != "sess" || d.Bytes == 0 || d.Cleared {
		t.Errorf("event data = %+v, want sid=sess Bytes>0 Cleared=false", d)
	}

	// Clear path: empty content → Cleared=true.
	captured = nil
	res, err = pt.Handler()(contextWith("sess", em), "", `{"content":""}`)
	if err != nil || res.IsError {
		t.Fatalf("clear: err=%v isErr=%v", err, res.IsError)
	}
	if !strings.Contains(toolResultText(res), "cleared") {
		t.Errorf("clear msg = %q", toolResultText(res))
	}
	if d, _ := captured[0].Data.(schema.WorkspacePlanUpdatedData); !d.Cleared || d.Bytes != 0 {
		t.Errorf("clear event = %+v, want Cleared=true Bytes=0", d)
	}
}

// TestPlanTool_RejectsOversize forwards workspace.ErrTooLarge as an error
// result (not a returned error — the LLM sees a textual failure).
func TestPlanTool_RejectsOversize(t *testing.T) {
	pt, _, _ := newTempPlanTool(t)
	huge := strings.Repeat("x", workspace.MaxPlanBytes+1)
	res, err := pt.Handler()(contextWith("sess", nil), "", `{"content":"`+huge+`"}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.IsError || !strings.Contains(toolResultText(res), "exceeds limit") {
		t.Errorf("oversize result = %+v, want IsError with 'exceeds limit'", res)
	}
}

// toolResultText extracts the concatenated text from a ToolResult's
// Content parts (treats it as a single text block, matching how
// TextResult / ErrorResult build their payloads).
func toolResultText(r schema.ToolResult) string {
	var sb strings.Builder
	for _, p := range r.Content {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// TestRegisterPlan exercises the public Register helper with a real registry.
func TestRegisterPlan(t *testing.T) {
	ws, _ := workspace.NewFileWorkspace(t.TempDir())
	reg := tool.NewRegistry()
	if err := RegisterPlan(reg, ws); err != nil {
		t.Fatalf("RegisterPlan: %v", err)
	}
	if _, ok := reg.Get(PlanToolName); !ok {
		t.Errorf("plan_update not registered")
	}
	if err := RegisterPlan(reg, nil); err == nil {
		t.Errorf("RegisterPlan(nil) = nil, want error")
	}
}
