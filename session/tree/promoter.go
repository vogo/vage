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

package tree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// Promoter generates a summary for a parent node by aggregating the
// information in its children. Implementations MUST tolerate len(children)==0
// by returning (parent.Summary, nil) — PromoteNode short-circuits before
// calling, but the Promoter contract still permits the empty input so
// callers can compose Promoters defensively.
//
// The store clamps the returned string to SummaryMaxBytes; implementations
// should aim for that target but need not enforce it.
type Promoter interface {
	Summarize(ctx context.Context, parent *TreeNode, children []*TreeNode) (string, error)
}

// PromoteFunc adapts a plain function to the Promoter interface, primarily
// for tests.
type PromoteFunc func(ctx context.Context, parent *TreeNode, children []*TreeNode) (string, error)

// Summarize implements Promoter.
func (f PromoteFunc) Summarize(ctx context.Context, parent *TreeNode, children []*TreeNode) (string, error) {
	return f(ctx, parent, children)
}

// NoopPromoter is a Promoter that returns parent.Summary unchanged. Use it
// when the goal is purely to mark children as folded without writing a new
// aggregate summary (e.g., experiments, costs control, manual UI flow).
type NoopPromoter struct{}

// Summarize returns the existing parent summary, even when there are
// children to fold. The store still flips child.Promoted=true as a result.
func (NoopPromoter) Summarize(_ context.Context, parent *TreeNode, _ []*TreeNode) (string, error) {
	if parent == nil {
		return "", nil
	}
	return parent.Summary, nil
}

// LLMPromoter calls a chat completion model to produce the new parent
// summary. The default system prompt asks for a concise paragraph; callers
// may override SystemPrompt for fine-tuning.
type LLMPromoter struct {
	// Client is the chat completer used to generate the new summary.
	// Required; nil triggers ErrInvalidArgument from Summarize.
	Client aimodel.ChatCompleter

	// Model overrides the model name on the request. Empty string passes
	// the request through with whatever default the client picks.
	Model string

	// SystemPrompt overrides the default system instructions. Empty uses
	// defaultLLMPromoterSystemPrompt.
	SystemPrompt string

	// MaxOutputTokens caps the requested output. 0 → 512.
	MaxOutputTokens int
}

const defaultLLMPromoterSystemPrompt = "" +
	"You are a senior assistant compressing sub-task notes into a single " +
	"paragraph that captures the parent task's progress. " +
	"Reply with a single concise paragraph (no headings, no lists, no " +
	"prefixes). Aim for at most 200 Chinese characters or roughly 600 bytes."

const defaultLLMPromoterMaxTokens = 512

// Summarize sends a request to the configured chat completer and returns
// the model's response text.
func (p *LLMPromoter) Summarize(ctx context.Context, parent *TreeNode, children []*TreeNode) (string, error) {
	if p == nil || p.Client == nil {
		return "", fmt.Errorf("%w: LLMPromoter requires a non-nil Client", ErrInvalidArgument)
	}
	if parent == nil {
		return "", nil
	}
	if len(children) == 0 {
		return parent.Summary, nil
	}

	system := p.SystemPrompt
	if system == "" {
		system = defaultLLMPromoterSystemPrompt
	}
	maxTok := p.MaxOutputTokens
	if maxTok <= 0 {
		maxTok = defaultLLMPromoterMaxTokens
	}

	req := &aimodel.ChatRequest{
		Model:     p.Model,
		MaxTokens: &maxTok,
		Messages: []aimodel.Message{
			{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent(system)},
			{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(buildLLMPromoterUserText(parent, children))},
		},
	}

	resp, err := p.Client.ChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("tree: LLMPromoter chat: %w", err)
	}
	return extractFirstChoiceText(resp), nil
}

// buildLLMPromoterUserText assembles the parent + children input. It is
// extracted for testability — assertions on the prompt body do not need
// to mock a chat client.
func buildLLMPromoterUserText(parent *TreeNode, children []*TreeNode) string {
	var b strings.Builder
	b.WriteString("PARENT\n")
	fmt.Fprintf(&b, "- Title: %s\n", parent.Title)
	fmt.Fprintf(&b, "- Status: %s\n", parent.Status)
	if parent.Summary != "" {
		fmt.Fprintf(&b, "- Existing summary: %s\n", parent.Summary)
	}
	fmt.Fprintf(&b, "\nCHILDREN (%d total)\n", len(children))
	for i, c := range children {
		title := c.Title
		summary := c.Summary
		if summary == "" {
			fmt.Fprintf(&b, "%d. [%s] [%s] %s\n", i+1, c.Type, c.Status, title)
		} else {
			fmt.Fprintf(&b, "%d. [%s] [%s] %s — %s\n", i+1, c.Type, c.Status, title, summary)
		}
	}
	return b.String()
}

// extractFirstChoiceText returns the first non-empty assistant text from
// resp. It returns "" when the response is malformed; the store falls back
// to parent.Summary in that case (the contract of "no error == valid new
// summary" still holds because empty is itself a valid empty summary).
func extractFirstChoiceText(resp *aimodel.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content.Text())
}

// CompressorPromoter delegates summarisation to a memory.ContextCompressor.
// Unlike LLMPromoter it does not call a chat model directly; instead it
// formats the children as a tiny conversation (one assistant message per
// child) and asks the compressor to fit them into a token budget that
// approximates SummaryMaxBytes.
//
// The output is the concatenation of the compressed messages' text — this
// is intentionally simple. Callers that want richer behaviour should
// implement Promoter directly.
type CompressorPromoter struct {
	// Compressor is required; nil triggers ErrInvalidArgument.
	Compressor memory.ContextCompressor

	// MaxBytes caps the desired output size. 0 → SummaryMaxBytes.
	MaxBytes int
}

// approxBytesPerToken is the (conservative, English-leaning) ratio used to
// translate MaxBytes into a token budget for the compressor. Chinese text
// is denser per byte (~1.2 byte/token) but compressors usually overshoot,
// so a fixed 4 keeps the math simple and the output close to target.
const approxBytesPerToken = 4

// Summarize translates children into messages, asks the compressor to fit
// them into a byte-derived token budget, then concatenates the result.
func (p *CompressorPromoter) Summarize(ctx context.Context, parent *TreeNode, children []*TreeNode) (string, error) {
	if p == nil || p.Compressor == nil {
		return "", fmt.Errorf("%w: CompressorPromoter requires a non-nil Compressor", ErrInvalidArgument)
	}
	if parent == nil {
		return "", nil
	}
	if len(children) == 0 {
		return parent.Summary, nil
	}

	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = SummaryMaxBytes
	}
	tokenBudget := max(maxBytes/approxBytesPerToken, 1)

	msgs := compressorPromoterMessages(parent, children)
	out, err := p.Compressor.Compress(ctx, msgs, tokenBudget)
	if err != nil {
		return "", fmt.Errorf("tree: CompressorPromoter compress: %w", err)
	}
	return joinSchemaMessages(out), nil
}

// compressorPromoterMessages renders parent + children into a synthetic
// conversation that the compressor can chew on. The first system message
// preserves the parent context; each child becomes one assistant turn.
func compressorPromoterMessages(parent *TreeNode, children []*TreeNode) []schema.Message {
	out := make([]schema.Message, 0, len(children)+1)
	headBuilder := strings.Builder{}
	headBuilder.WriteString("Parent: ")
	headBuilder.WriteString(parent.Title)
	if parent.Summary != "" {
		headBuilder.WriteString(" — ")
		headBuilder.WriteString(parent.Summary)
	}
	out = append(out, schema.Message{Message: aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(headBuilder.String()),
	}})
	for _, c := range children {
		out = append(out, schema.Message{Message: aimodel.Message{
			Role:    aimodel.RoleAssistant,
			Content: aimodel.NewTextContent(childToCompressorBody(c)),
		}})
	}
	return out
}

// childToCompressorBody flattens one child's title + summary into a single
// line that fits the schema.Message.Content's plain string contract.
func childToCompressorBody(c *TreeNode) string {
	if c == nil {
		return ""
	}
	if c.Summary == "" {
		return fmt.Sprintf("[%s] [%s] %s", c.Type, c.Status, c.Title)
	}
	return fmt.Sprintf("[%s] [%s] %s — %s", c.Type, c.Status, c.Title, c.Summary)
}

// joinSchemaMessages concatenates message contents with single newlines,
// stripping any empty messages the compressor may emit. Result is suitable
// for storing in TreeNode.Summary.
func joinSchemaMessages(msgs []schema.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		text := m.Content.Text()
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String()
}

// clampSummary returns s clamped to maxBytes by truncating tail-first on a
// utf-8 boundary. Used by the store after Promoter.Summarize so each
// implementation does not have to repeat this logic.
//
// A negative or zero maxBytes is treated as "no limit" (the caller should
// have validated already; this is a defensive guard).
func clampSummary(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Truncate to a utf-8 boundary: walk back until the byte at the cut
	// is a starter (i.e., not a continuation byte 0b10xxxxxx).
	cut := maxBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// errPromoterNotConfigured is returned when a store is asked to PromoteNode
// without a Promoter. This is a sentinel separate from ErrInvalidArgument
// so callers can detect "wiring not done" distinctly.
var errPromoterNotConfigured = errors.New("tree: promoter is not configured")

// ErrPromoterNotConfigured is returned by PromoteNode when no Promoter has
// been wired into the store via WithMapPromoter / WithFilePromoter.
func ErrPromoterNotConfigured() error { return errPromoterNotConfigured }
