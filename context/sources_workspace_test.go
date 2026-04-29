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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/workspace"
)

// stubWorkspace is a Workspace double we can drive into the three states
// the source recognises (skipped / ok / error). We do NOT use the real
// FileWorkspace here because the goal is to assert the source's branching
// logic, not the file IO.
type stubWorkspace struct {
	plan     string
	notes    []workspace.NoteInfo
	planErr  error
	notesErr error
}

func (s *stubWorkspace) ReadPlan(_ context.Context, _ string) (string, error) {
	return s.plan, s.planErr
}
func (s *stubWorkspace) WritePlan(_ context.Context, _, _ string) error { return nil }
func (s *stubWorkspace) ReadNote(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (s *stubWorkspace) WriteNote(_ context.Context, _, _, _ string) error { return nil }
func (s *stubWorkspace) ListNotes(_ context.Context, _ string) ([]workspace.NoteInfo, error) {
	return s.notes, s.notesErr
}
func (s *stubWorkspace) Delete(_ context.Context, _ string) error { return nil }
func (s *stubWorkspace) PathOf(_ string) string                   { return "" }

func TestWorkspaceSource_NilOrNoSession_Skipped(t *testing.T) {
	src := &WorkspaceSource{} // nil Workspace
	got, err := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if err != nil || got.Report.Status != StatusSkipped {
		t.Fatalf("nil ws: %+v / %v", got, err)
	}

	src.Workspace = &stubWorkspace{}
	got, _ = src.Fetch(context.Background(), FetchInput{SessionID: ""})
	if got.Report.Status != StatusSkipped {
		t.Errorf("empty sid status = %q, want skipped", got.Report.Status)
	}
}

func TestWorkspaceSource_EmptyWorkspace_Skipped(t *testing.T) {
	src := &WorkspaceSource{Workspace: &stubWorkspace{}}
	got, _ := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if got.Report.Status != StatusSkipped {
		t.Errorf("empty workspace status = %q, want skipped", got.Report.Status)
	}
	if len(got.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(got.Messages))
	}
}

func TestWorkspaceSource_PlanReadError_Error(t *testing.T) {
	src := &WorkspaceSource{Workspace: &stubWorkspace{planErr: errors.New("io fail")}}
	got, err := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Report.Status != StatusError {
		t.Errorf("status = %q, want error", got.Report.Status)
	}
	if got.Report.Error == "" {
		t.Errorf("Error empty, want non-empty")
	}
}

func TestWorkspaceSource_NotesError_Error(t *testing.T) {
	src := &WorkspaceSource{Workspace: &stubWorkspace{plan: "p", notesErr: errors.New("ls fail")}}
	got, _ := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if got.Report.Status != StatusError {
		t.Errorf("status = %q, want error", got.Report.Status)
	}
}

func TestWorkspaceSource_OK_RendersPlanAndIndex(t *testing.T) {
	notes := []workspace.NoteInfo{
		{Name: "alpha", Bytes: 100},
		{Name: "beta", Bytes: 2048},
	}
	src := &WorkspaceSource{Workspace: &stubWorkspace{plan: "# plan\n- step 1", notes: notes}}
	got, err := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Report.Status != StatusOK {
		t.Errorf("status = %q, want ok", got.Report.Status)
	}
	if got.Report.OutputN != 1 || len(got.Messages) != 1 {
		t.Errorf("OutputN=%d msgs=%d", got.Report.OutputN, len(got.Messages))
	}
	text := got.Messages[0].Content.Text()
	for _, want := range []string{"## Plan Workspace", "### Plan", "step 1", "alpha.md", "beta.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q\n--- text ---\n%s", want, text)
		}
	}
}

func TestWorkspaceSource_OnlyPlan_RendersWithoutNotesSection(t *testing.T) {
	src := &WorkspaceSource{Workspace: &stubWorkspace{plan: "# only plan"}}
	got, _ := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if got.Report.Status != StatusOK {
		t.Errorf("status = %q", got.Report.Status)
	}
	text := got.Messages[0].Content.Text()
	if strings.Contains(text, "### Notes") {
		t.Errorf("notes section appeared with no notes\n%s", text)
	}
}

func TestWorkspaceSource_OnlyNotes_RendersEmptyPlanHint(t *testing.T) {
	src := &WorkspaceSource{Workspace: &stubWorkspace{notes: []workspace.NoteInfo{{Name: "n"}}}}
	got, _ := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	text := got.Messages[0].Content.Text()
	if !strings.Contains(text, "(empty — call `plan_update`") {
		t.Errorf("missing empty-plan hint\n%s", text)
	}
	if !strings.Contains(text, "n.md") {
		t.Errorf("missing note entry\n%s", text)
	}
}

// TestWorkspaceSource_TruncatesOversizedPlan checks that a plan larger than
// MaxBytes is tail-preserving truncated and the report status flips to
// StatusTruncated. We seed the plan with a distinct head ('H') and tail
// ('T') so we can assert the surviving portion is the tail (most recent
// edits) and the truncation marker is included.
func TestWorkspaceSource_TruncatesOversizedPlan(t *testing.T) {
	const headLen = 90
	const tailLen = 10
	plan := strings.Repeat("H", headLen) + strings.Repeat("T", tailLen)
	src := &WorkspaceSource{
		Workspace: &stubWorkspace{plan: plan},
		MaxBytes:  tailLen,
	}
	got, _ := src.Fetch(context.Background(), FetchInput{SessionID: "sess"})
	if got.Report.Status != StatusTruncated {
		t.Errorf("status = %q, want truncated", got.Report.Status)
	}
	text := got.Messages[0].Content.Text()
	if strings.Count(text, "H") != 0 {
		t.Errorf("head bytes leaked into truncated plan\n--- text ---\n%s", text)
	}
	if got, want := strings.Count(text, "T"), tailLen; got != want {
		t.Errorf("expected %d T's in tail, got %d\n--- text ---\n%s", want, got, text)
	}
	if !strings.Contains(text, "earlier portion of plan.md elided") {
		t.Errorf("missing truncation marker\n--- text ---\n%s", text)
	}
}

// TestHumanBytes verifies the rendering helper produces stable output across
// the three magnitude bands.
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		bytes int
		want  string
	}{
		{0, "0 bytes"},
		{500, "500 bytes"},
		{2048, "2.0 KB"},
		{2 * 1024 * 1024, "2.0 MB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.bytes); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestHumanAge_ZeroIsQuestionMark confirms a zero time.Time renders as "?"
// so the prompt does not show a misleading age for un-stamped notes.
func TestHumanAge_ZeroIsQuestionMark(t *testing.T) {
	var zero time.Time
	if got := humanAge(zero); got != "?" {
		t.Errorf("humanAge(zero) = %q, want '?'", got)
	}
}
