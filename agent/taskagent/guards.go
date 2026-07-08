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

package taskagent

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/schema"
)

// runInputGuards checks user input through input guards.
// Returns the (possibly rewritten) text content, or a BlockedError.
func (a *Agent) runInputGuards(ctx context.Context, req *schema.RunRequest) error {
	if len(a.inputGuards) == 0 || len(req.Messages) == 0 {
		return nil
	}

	// Find the last user message.
	idx := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == aimodel.RoleUser {
			idx = i
			break
		}
	}

	if idx < 0 {
		return nil
	}

	msg := &guard.Message{
		Direction: guard.DirectionInput,
		Content:   req.Messages[idx].Content.Text(),
		AgentID:   a.ID(),
		SessionID: req.SessionID,
		Metadata:  req.Metadata,
	}

	result, err := guard.RunGuards(ctx, msg, a.inputGuards...)
	if err != nil {
		return err
	}

	if result.Action == guard.ActionBlock {
		return &guard.BlockedError{Result: result}
	}

	if result.Action == guard.ActionRewrite {
		req.Messages[idx].Content = aimodel.NewTextContent(msg.Content)
	}

	return nil
}

// runOutputGuards checks agent output through output guards.
// Returns the (possibly rewritten) text, or a BlockedError.
func (a *Agent) runOutputGuards(ctx context.Context, sessionID string, respMsgs []schema.Message) ([]schema.Message, error) {
	if len(a.outputGuards) == 0 || len(respMsgs) == 0 {
		return respMsgs, nil
	}

	text := respMsgs[0].Content.Text()

	msg := &guard.Message{
		Direction: guard.DirectionOutput,
		Content:   text,
		AgentID:   a.ID(),
		SessionID: sessionID,
		Metadata:  nil,
	}

	result, err := guard.RunGuards(ctx, msg, a.outputGuards...)
	if err != nil {
		return nil, err
	}

	if result.Action == guard.ActionBlock {
		return nil, &guard.BlockedError{Result: result}
	}

	if result.Action == guard.ActionRewrite {
		respMsgs[0].Content = aimodel.NewTextContent(msg.Content)
	}

	return respMsgs, nil
}

// runToolResultGuards scans the text of a tool result through toolResultGuards
// and returns the (possibly rewritten or error-replaced) ToolResult along with
// an optional guard_check event for the caller to dispatch via the appropriate
// channel (hook-only in Run, stream+hook in RunStream). A nil event means the
// guard produced no material outcome.
// When no guards are configured, IsError results, or non-text content, the
// input is returned unchanged and evt is nil.
func (a *Agent) runToolResultGuards(ctx context.Context, rc *runContext, tc aimodel.ToolCall, result schema.ToolResult) (schema.ToolResult, *schema.Event) {
	if len(a.toolResultGuards) == 0 {
		return result, nil
	}

	if result.IsError {
		return result, nil
	}

	textIdx := -1
	for i, p := range result.Content {
		if p.Type == "text" {
			textIdx = i
			break
		}
	}

	if textIdx < 0 {
		return result, nil
	}

	text := result.Content[textIdx].Text
	if text == "" {
		return result, nil
	}

	msg := &guard.Message{
		Direction: guard.DirectionToolResult,
		Content:   text,
		AgentID:   a.ID(),
		SessionID: rc.sessionID,
		Metadata: map[string]any{
			guard.MetaToolCallID: tc.ID,
			guard.MetaToolName:   tc.Function.Name,
		},
	}

	gres, err := guard.RunGuards(ctx, msg, a.toolResultGuards...)
	if err != nil {
		evt := a.buildGuardCheckEvent(rc, tc, text, "tool_result_injection", "error", nil, "", err.Error())
		return schema.ErrorResult(tc.ID, "tool result guard error: "+err.Error()), &evt
	}

	switch gres.Action {
	case guard.ActionPass:
		if len(gres.Violations) == 0 {
			return result, nil
		}
		// Log-only outcome: violations surfaced but content unchanged.
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "log", gres.Violations, sev, "")
		return result, &evt
	case guard.ActionRewrite:
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "rewrite", gres.Violations, sev, gres.Reason)

		out := result
		out.Content = make([]schema.ContentPart, len(result.Content))
		copy(out.Content, result.Content)
		out.Content[textIdx].Text = gres.Content
		return out, &evt
	case guard.ActionBlock:
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "block", gres.Violations, sev, gres.Reason)

		reason := gres.Reason
		if reason == "" {
			reason = "tool result blocked"
		}

		return schema.ErrorResult(tc.ID, fmt.Sprintf("blocked by %s: %s", gres.GuardName, reason)), &evt
	default:
		return result, nil
	}
}

// maxToolResultSeverity returns the highest severity name among rule hits,
// or "" if none. It consults any ToolResultInjectionGuard among the
// configured tool-result guards to resolve rule → severity mapping.
func (a *Agent) maxToolResultSeverity(hits []string) string {
	var max guard.Severity

	for _, g := range a.toolResultGuards {
		tg, ok := g.(*guard.ToolResultInjectionGuard)
		if !ok {
			continue
		}

		if s := tg.MaxSeverity(hits); s > max {
			max = s
		}
	}

	if max == 0 {
		return ""
	}

	return max.String()
}

// buildGuardCheckEvent creates an EventGuardCheck event and writes a
// structured warning log as a side effect. The returned event must be
// dispatched by the caller via the channel appropriate for the execution
// mode (a.dispatch for non-streaming, send for streaming).
func (a *Agent) buildGuardCheckEvent(rc *runContext, tc aimodel.ToolCall, text, guardName, action string, hits []string, severity, reason string) schema.Event {
	const snippetMax = 200

	snippet := safeSnippet(text, snippetMax)

	attrs := []any{
		"guard", guardName,
		"tool", tc.Function.Name,
		"tool_call_id", tc.ID,
		"action", action,
		"rules", hits,
		"agent_id", a.ID(),
		"session_id", rc.sessionID,
	}
	if severity != "" {
		attrs = append(attrs, "severity", severity)
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}

	slog.Warn("vage: tool result guard hit", attrs...)

	return schema.NewEvent(schema.EventGuardCheck, a.ID(), rc.sessionID, schema.GuardCheckData{
		GuardName:  guardName,
		ToolCallID: tc.ID,
		ToolName:   tc.Function.Name,
		Action:     action,
		RuleHits:   hits,
		Severity:   severity,
		Snippet:    snippet,
	})
}

// safeSnippet returns the leading bytes of s up to max, walking back to the
// nearest rune boundary so the result is always valid UTF-8. An ellipsis
// marker is appended when truncation happened.
func safeSnippet(s string, max int) string {
	if len(s) <= max {
		return s
	}

	// Walk back until we land on a rune boundary. We never walk more than 3
	// bytes because a UTF-8 codepoint is at most 4 bytes.
	end := max
	for end > 0 && end > max-4 && !utf8.RuneStart(s[end]) {
		end--
	}

	return s[:end] + "..."
}
