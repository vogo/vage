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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/workspace"
)

// WorkspaceSource projects the per-session Plan Workspace (plan.md plus the
// notes index) into a single system-role message. It is the read-side
// counterpart to the plan_update / notes_write tools: writes go through
// the tools, reads happen here every turn so the LLM always sees the
// current plan plus a concise list of available notes.
//
// This source is **optional**: a nil Workspace, an empty SessionID, or an
// empty workspace all surface as Status="skipped" without an error.
type WorkspaceSource struct {
	// Workspace is the per-process workspace handle. nil disables the source.
	Workspace workspace.Workspace
	// MaxBytes caps how many bytes of plan.md to inject (0 = use
	// workspace.MaxPlanBytes). When plan.md exceeds the cap the leading
	// bytes are dropped so the **tail** survives — LLMs typically append
	// new steps at the bottom of plan.md, and the most recent progress is
	// what the next turn most needs to see. A short marker is prepended to
	// signal the truncation explicitly to the model.
	MaxBytes int
}

// Compile-time interface conformance.
var _ Source = (*WorkspaceSource)(nil)

// Name returns SourceNameWorkspace.
func (s *WorkspaceSource) Name() string { return SourceNameWorkspace }

// Fetch reads plan.md and the notes index for the current session and
// renders them into one system message. Both reads must succeed for the
// source to emit content; either failure flips Status to "error" but the
// Builder fail-open contract keeps the rest of the pipeline running.
func (s *WorkspaceSource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameWorkspace}

	if s.Workspace == nil || in.SessionID == "" {
		rep.Status = StatusSkipped
		rep.Note = "no workspace / no session"
		return FetchResult{Report: rep}, nil
	}

	plan, err := s.Workspace.ReadPlan(ctx, in.SessionID)
	if err != nil {
		slog.Warn("vctx: workspace read plan", "session_id", in.SessionID, "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	notes, err := s.Workspace.ListNotes(ctx, in.SessionID)
	if err != nil {
		slog.Warn("vctx: workspace list notes", "session_id", in.SessionID, "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	if plan == "" && len(notes) == 0 {
		rep.Status = StatusSkipped
		rep.Note = "empty workspace"
		return FetchResult{Report: rep}, nil
	}

	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = workspace.MaxPlanBytes
	}
	truncated := false
	if len(plan) > maxBytes {
		// Tail-preserving truncation: drop the leading bytes so the most
		// recent edits (typically appended at the bottom of plan.md)
		// survive. A short marker is prepended so the LLM does not mistake
		// the partial text for the full plan.
		plan = plan[len(plan)-maxBytes:]
		truncated = true
	}

	text := renderWorkspace(plan, notes, truncated)

	rep.OriginalCount = len(plan) + indexBytes(notes)
	rep.OutputN = 1
	if truncated {
		rep.Status = StatusTruncated
		rep.DroppedN = 1
	} else {
		rep.Status = StatusOK
	}

	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}
	return FetchResult{Messages: []aimodel.Message{msg}, Report: rep}, nil
}

// renderWorkspace formats plan + notes index into a single Markdown block.
// Layout intentionally mimics how a human would write a memo so the LLM
// reads it with minimal token overhead and treats it as authoritative.
// truncated=true prepends an explicit marker so the LLM knows the plan
// shown is the tail of a larger document and earlier history was elided.
func renderWorkspace(plan string, notes []workspace.NoteInfo, truncated bool) string {
	var b strings.Builder
	b.WriteString("## Plan Workspace\n")
	b.WriteString("(Persisted across sessions. Use `plan_update` to rewrite plan.md, ")
	b.WriteString("`notes_write` to capture facts, `notes_read` to recall a specific note.)\n\n")

	if plan != "" {
		b.WriteString("### Plan\n")
		if truncated {
			b.WriteString("(... earlier portion of plan.md elided; tail shown ...)\n")
		}
		b.WriteString(plan)
		if !strings.HasSuffix(plan, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	} else {
		b.WriteString("### Plan\n(empty — call `plan_update` to record progress)\n\n")
	}

	if len(notes) > 0 {
		b.WriteString("### Notes\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s.md (%s, %s)\n", n.Name, humanBytes(n.Bytes), humanAge(n.UpdatedAt))
		}
		b.WriteString("\n(Use `notes_read {\"name\":\"...\"}` to view a note's full body.)\n")
	}

	return b.String()
}

// indexBytes is a rough byte count of the notes-index payload, used for
// the report's OriginalCount metric.
func indexBytes(notes []workspace.NoteInfo) int {
	n := 0
	for _, e := range notes {
		// name + ".md (" + bytes + ", " + age + ")\n" — approximate.
		n += len(e.Name) + 16
	}
	return n
}

// humanBytes prints a byte count in a compact form. The cutoffs are chosen
// to give the LLM a quick "is this big or small" signal without precision
// games — operators wanting exact bytes can hit the HTTP listing endpoint.
func humanBytes(b int) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%d bytes", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	}
}

// humanAge prints an approximate "how long ago" — accuracy matters less
// than readability since the LLM only uses this to decide whether to
// re-read a note.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
