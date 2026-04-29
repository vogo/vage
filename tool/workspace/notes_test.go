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

// TestNotesWriteTool_Handler covers the canonical write path, the missing
// session id error, and the path-traversal rejection that protects the
// workspace from a forged note name.
func TestNotesWriteTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	wt := NewNotesWriteTool(ws)

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

	// Path traversal: tool returns IsError result; nothing on disk.
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"../passwd","content":"x"}`)
	if !res.IsError || !strings.Contains(toolResultText(res), "invalid note name") {
		t.Errorf("path traversal: want IsError with 'invalid note name', got %+v", res)
	}

	// Happy path.
	captured = nil
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"design","content":"# decisions"}`)
	if res.IsError {
		t.Fatalf("write: %+v", res)
	}
	got, _ := ws.ReadNote(context.Background(), "sess", "design")
	if got != "# decisions" {
		t.Errorf("ReadNote = %q", got)
	}
	if len(captured) != 1 {
		t.Fatalf("events captured = %d, want 1", len(captured))
	}
	if d, ok := captured[0].Data.(schema.WorkspaceNoteWrittenData); !ok ||
		d.Name != "design" || d.Cleared || d.Bytes == 0 {
		t.Errorf("event = %+v", captured[0])
	}

	// Delete via empty content.
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"design","content":""}`)
	if res.IsError {
		t.Fatalf("delete: %+v", res)
	}
	got, _ = ws.ReadNote(context.Background(), "sess", "design")
	if got != "" {
		t.Errorf("ReadNote post-delete = %q, want empty", got)
	}
}

// TestNotesReadTool_Handler covers the read path and the empty-note response.
func TestNotesReadTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	rt := NewNotesReadTool(ws)
	wt := NewNotesWriteTool(ws)

	// Missing sid.
	res, _ := rt.Handler()(context.Background(), "", `{"name":"x"}`)
	if !res.IsError {
		t.Fatalf("missing sid: want IsError=true, got %+v", res)
	}

	// Read missing → "(no note ...)".
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"x"}`)
	if res.IsError {
		t.Fatalf("missing note: unexpected error: %+v", res)
	}
	if !strings.Contains(toolResultText(res), "no note") {
		t.Errorf("missing-note message = %q", toolResultText(res))
	}

	// Write then read.
	_, _ = wt.Handler()(contextWith("sess", nil), "", `{"name":"alpha","content":"hello"}`)
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"alpha"}`)
	if res.IsError || toolResultText(res) != "hello" {
		t.Errorf("read = %+v / %q, want 'hello'", res, toolResultText(res))
	}

	// Bad name.
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"../etc"}`)
	if !res.IsError {
		t.Errorf("bad name: want IsError=true, got %+v", res)
	}
}

// TestRegisterNotes wires both notes_write and notes_read into a registry.
func TestRegisterNotes(t *testing.T) {
	ws, _ := workspace.NewFileWorkspace(t.TempDir())
	reg := tool.NewRegistry()
	if err := RegisterNotes(reg, ws); err != nil {
		t.Fatalf("RegisterNotes: %v", err)
	}
	for _, n := range []string{NotesWriteToolName, NotesReadToolName} {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
	if err := RegisterNotes(reg, nil); err == nil {
		t.Errorf("RegisterNotes(nil) = nil, want error")
	}
}
