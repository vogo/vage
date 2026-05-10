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

// TestArtifactWriteTool_Handler covers write path + event payload + path
// traversal rejection.
func TestArtifactWriteTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	wt := NewArtifactWriteTool(ws)

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

	// Path traversal.
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"../../etc","content":"x"}`)
	if !res.IsError || !strings.Contains(toolResultText(res), "invalid note name") {
		t.Errorf("path traversal: want IsError, got %+v", res)
	}

	// Happy path.
	captured = nil
	res, _ = wt.Handler()(contextWith("sess", em), "", `{"name":"diff.patch","content":"---\n+++ "}`)
	if res.IsError {
		t.Fatalf("write: %+v", res)
	}
	got, _ := ws.ReadArtifact(context.Background(), "sess", "diff.patch")
	if string(got) != "---\n+++ " {
		t.Errorf("ReadArtifact = %q", got)
	}
	if len(captured) != 1 {
		t.Fatalf("events captured = %d, want 1", len(captured))
	}
	d, ok := captured[0].Data.(schema.WorkspaceArtifactWrittenData)
	if !ok {
		t.Fatalf("event payload = %T, want WorkspaceArtifactWrittenData", captured[0].Data)
	}
	if d.Name != "diff.patch" || d.Bytes == 0 || d.Path == "" {
		t.Errorf("event = %+v", d)
	}
}

// TestArtifactReadTool_Handler covers read + missing-artifact response.
func TestArtifactReadTool_Handler(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.NewFileWorkspace(root)
	rt := NewArtifactReadTool(ws)

	// Missing sid.
	res, _ := rt.Handler()(context.Background(), "", `{"name":"x"}`)
	if !res.IsError {
		t.Fatalf("missing sid: want IsError=true, got %+v", res)
	}

	// Read missing → friendly message.
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"missing"}`)
	if res.IsError {
		t.Fatalf("missing artifact: unexpected error: %+v", res)
	}
	if !strings.Contains(toolResultText(res), "no artifact") {
		t.Errorf("missing message = %q", toolResultText(res))
	}

	// Write directly via the workspace API, then read via tool.
	_, _ = ws.WriteArtifact(context.Background(), "sess", "report.txt", []byte("payload"))
	res, _ = rt.Handler()(contextWith("sess", nil), "", `{"name":"report.txt"}`)
	if res.IsError || toolResultText(res) != "payload" {
		t.Errorf("read = %+v / %q", res, toolResultText(res))
	}
}

// TestRegisterArtifacts wires both artifact_* tools into a registry.
func TestRegisterArtifacts(t *testing.T) {
	ws, _ := workspace.NewFileWorkspace(t.TempDir())
	reg := tool.NewRegistry()
	if err := RegisterArtifacts(reg, ws); err != nil {
		t.Fatalf("RegisterArtifacts: %v", err)
	}
	for _, n := range []string{ArtifactWriteToolName, ArtifactReadToolName} {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
}

func TestRegisterArtifacts_RejectsNilWS(t *testing.T) {
	reg := tool.NewRegistry()
	if err := RegisterArtifacts(reg, nil); err == nil {
		t.Errorf("expected error on nil ws")
	}
}
