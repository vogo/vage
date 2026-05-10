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

// TestScratchWriteTool_Handler covers the canonical write path, missing
// sid rejection, slot binding (entries written go to the bound slot
// regardless of what the LLM passes — there is no slot input), and the
// path-traversal rejection.
func TestScratchWriteTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	wt := NewScratchWriteTool(ws, "child-a")

	var captured []schema.Event
	em := schema.Emitter(func(e schema.Event) error {
		captured = append(captured, e)
		return nil
	})

	// Missing sid.
	res, _ := wt.Handler()(context.Background(), "", `{"name":"a","content":"b"}`)
	if !res.IsError {
		t.Fatalf("missing sid: want IsError=true, got %+v", res)
	}

	// Path traversal in entry name.
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"../passwd","content":"x"}`)
	if !res.IsError || !strings.Contains(toolResultText(res), "invalid note name") {
		t.Errorf("path traversal: want IsError, got %+v", res)
	}

	// Happy path.
	captured = nil
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"draft","content":"# rough"}`)
	if res.IsError {
		t.Fatalf("write: %+v", res)
	}
	got, _ := ws.ReadScratch(context.Background(), "sess", "child-a", "draft")
	if got != "# rough" {
		t.Errorf("ReadScratch = %q", got)
	}
	// Verify the entry is in the bound slot, not somewhere else.
	other, _ := ws.ReadScratch(context.Background(), "sess", "child-b", "draft")
	if other != "" {
		t.Errorf("entry leaked to other slot: %q", other)
	}
	if len(captured) != 1 {
		t.Fatalf("events captured = %d, want 1", len(captured))
	}
	if d, ok := captured[0].Data.(schema.WorkspaceScratchWrittenData); !ok ||
		d.Slot != "child-a" || d.Name != "draft" || d.Cleared || d.Bytes == 0 {
		t.Errorf("event = %+v", captured[0])
	}

	// Delete via empty content.
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"draft","content":""}`)
	if res.IsError {
		t.Fatalf("delete: %+v", res)
	}
	got, _ = ws.ReadScratch(context.Background(), "sess", "child-a", "draft")
	if got != "" {
		t.Errorf("ReadScratch post-delete = %q", got)
	}
}

// TestScratchReadTool_Handler covers read of an entry written via the
// workspace API directly (the tool's only knob is the bound slot).
func TestScratchReadTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	rt := NewScratchReadTool(ws, "child-a")

	// Missing sid.
	res, _ := rt.Handler()(context.Background(), "", `{"name":"x"}`)
	if !res.IsError {
		t.Fatalf("missing sid: want IsError=true, got %+v", res)
	}

	// Read missing → friendly message, not error.
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"x"}`)
	if res.IsError {
		t.Fatalf("missing entry: unexpected error: %+v", res)
	}
	if !strings.Contains(toolResultText(res), "no scratch entry") {
		t.Errorf("missing-entry message = %q", toolResultText(res))
	}

	// Write then read.
	_ = ws.WriteScratch(context.Background(), "sess", "child-a", "alpha", "hi")
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"alpha"}`)
	if res.IsError || toolResultText(res) != "hi" {
		t.Errorf("read = %+v / %q", res, toolResultText(res))
	}

	// Read of an entry in another slot must NOT see it (bound slot).
	_ = ws.WriteScratch(context.Background(), "sess", "child-b", "beta", "secret")
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"beta"}`)
	if res.IsError || !strings.Contains(toolResultText(res), "no scratch entry") {
		t.Errorf("cross-slot leak: %+v / %q", res, toolResultText(res))
	}
}

// TestScratchListTool_Handler covers the empty case and the index format.
func TestScratchListTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	lt := NewScratchListTool(ws, "child-a")

	// Missing sid.
	res, _ := lt.Handler()(context.Background(), "", `{}`)
	if !res.IsError {
		t.Fatalf("missing sid: want IsError, got %+v", res)
	}

	// Empty slot.
	res, _ = lt.Handler()(contextWith("sess", nil), "", `{}`)
	if res.IsError || !strings.Contains(toolResultText(res), "no entries") {
		t.Errorf("empty: %+v / %q", res, toolResultText(res))
	}

	// Populate two entries.
	_ = ws.WriteScratch(context.Background(), "sess", "child-a", "first", "1")
	_ = ws.WriteScratch(context.Background(), "sess", "child-a", "second", "2")
	res, _ = lt.Handler()(contextWith("sess", nil), "", `{}`)
	if res.IsError {
		t.Fatalf("list: %+v", res)
	}
	body := toolResultText(res)
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
		t.Errorf("listing missing entries: %q", body)
	}
}

// TestRegisterScratch wires all three scratch_* tools into a registry.
func TestRegisterScratch(t *testing.T) {
	ws, _ := workspace.NewFileWorkspace(t.TempDir())
	reg := tool.NewRegistry()
	if err := RegisterScratch(reg, ws, "slot-1"); err != nil {
		t.Fatalf("RegisterScratch: %v", err)
	}
	for _, n := range []string{ScratchWriteToolName, ScratchReadToolName, ScratchListToolName} {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
}

// TestRegisterScratch_RejectsEmptySlot ensures the slot is mandatory at
// registration so callers cannot accidentally bind to "" and have the
// workspace validation reject every call at runtime.
func TestRegisterScratch_RejectsEmptySlot(t *testing.T) {
	ws, _ := workspace.NewFileWorkspace(t.TempDir())
	reg := tool.NewRegistry()
	if err := RegisterScratch(reg, ws, ""); err == nil {
		t.Errorf("expected error on empty slot")
	}
}

func TestRegisterScratch_RejectsNilWS(t *testing.T) {
	reg := tool.NewRegistry()
	if err := RegisterScratch(reg, nil, "slot"); err == nil {
		t.Errorf("expected error on nil ws")
	}
}
