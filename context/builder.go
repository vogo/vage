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
	"log/slog"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// DefaultBuilderName is the BuilderName written into BuildReport.BuilderName
// unless overridden by WithName.
const DefaultBuilderName = "default"

// DefaultBuilder is the standard Builder implementation. It runs sources in
// declared order, charging must-include sources first and then handing the
// remaining budget to optional sources via greedy assignment.
type DefaultBuilder struct {
	name      string
	sources   []Source
	estimator memory.TokenEstimator
	hooks     *hook.Manager
}

// Compile-time interface conformance.
var _ Builder = (*DefaultBuilder)(nil)

// Option configures a DefaultBuilder.
type Option func(*DefaultBuilder)

// WithName overrides the builder name used in BuildReport and the emitted
// EventContextBuilt event.
func WithName(name string) Option {
	return func(b *DefaultBuilder) { b.name = name }
}

// WithSource appends a single source to the builder. Sources execute in
// the order they are added.
func WithSource(s Source) Option {
	return func(b *DefaultBuilder) {
		if s != nil {
			b.sources = append(b.sources, s)
		}
	}
}

// WithSources appends a list of sources to the builder.
func WithSources(sources ...Source) Option {
	return func(b *DefaultBuilder) {
		for _, s := range sources {
			if s != nil {
				b.sources = append(b.sources, s)
			}
		}
	}
}

// WithTokenEstimator overrides the default token estimator
// (memory.DefaultTokenEstimator) used for budget accounting and trim
// fallback.
func WithTokenEstimator(est memory.TokenEstimator) Option {
	return func(b *DefaultBuilder) {
		if est != nil {
			b.estimator = est
		}
	}
}

// WithHookManager wires the builder to a hook.Manager so EventContextBuilt
// is dispatched on every successful Build. nil is allowed and disables
// dispatch.
func WithHookManager(m *hook.Manager) Option {
	return func(b *DefaultBuilder) { b.hooks = m }
}

// NewDefaultBuilder constructs a DefaultBuilder with the given options.
// With no options it has zero sources and produces an empty Build output —
// useful primarily as a starting point in tests.
func NewDefaultBuilder(opts ...Option) *DefaultBuilder {
	b := &DefaultBuilder{
		name:      DefaultBuilderName,
		estimator: memory.DefaultTokenEstimator,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Name returns the builder's configured name.
func (b *DefaultBuilder) Name() string { return b.name }

// Build runs each source in declaration order, applies the must-include /
// optional budget split, dispatches EventContextBuilt, and returns the
// assembled message list with the audit report.
//
// Source errors are fail-open: a slog.Warn is emitted, the source is
// recorded with Status="error", and execution continues. The exception is
// SystemPromptSource, which returns a fatal error if its template fails to
// render — that bubbles up as Build's error.
func (b *DefaultBuilder) Build(ctx context.Context, in BuildInput) (BuildResult, error) {
	if err := ctx.Err(); err != nil {
		return BuildResult{}, err
	}

	start := time.Now()

	// Pre-classify sources so we run them in two passes (must-include then
	// optional) while still honouring declaration order within each group.
	type slot struct {
		idx  int
		src  Source
		must bool
	}

	slots := make([]slot, len(b.sources))
	for i, s := range b.sources {
		slots[i] = slot{idx: i, src: s, must: isMustInclude(s)}
	}

	// Per-source emitted messages and reports, slotted by declaration index
	// so the final output is always in declaration order regardless of when
	// each source executed.
	emitted := make([][]aimodel.Message, len(b.sources))
	reports := make([]schema.ContextSourceReport, len(b.sources))

	mustTokens := 0
	droppedTotal := 0

	// Pass 1: must-include sources, unbounded budget.
	for _, sl := range slots {
		if !sl.must {
			continue
		}

		fin := fromBuildInput(in)
		fin.Budget = 0

		msgs, rep, err := b.runSource(ctx, sl.src, fin)
		if err != nil {
			// Must-include sources can choose to be fail-closed (e.g. system
			// prompt render failures); propagate up.
			return BuildResult{}, err
		}

		emitted[sl.idx] = msgs
		reports[sl.idx] = rep
		mustTokens += rep.Tokens
	}

	// Pass 2: optional sources, greedy on remaining budget.
	remaining := 0
	if in.Budget > 0 {
		remaining = max(in.Budget-mustTokens, 0)
	}

	for _, sl := range slots {
		if sl.must {
			continue
		}

		// Respect context cancellation between optional sources so a
		// cancelled run does not keep iterating fetches.
		if err := ctx.Err(); err != nil {
			return BuildResult{}, err
		}

		fin := fromBuildInput(in)
		fin.Budget = remaining // 0 means unlimited when in.Budget is 0

		msgs, rep, err := b.runSource(ctx, sl.src, fin)
		if err != nil {
			// Context errors are not fail-open: surface cancellation /
			// deadline so the caller can react instead of silently
			// continuing to the next source.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return BuildResult{}, ctxErr
			}

			// Fail-open: log and mark report; never propagate.
			slog.Warn("vctx: source error",
				"builder", b.name, "source", sl.src.Name(), "error", err)
			rep.Source = sl.src.Name()
			rep.Status = StatusError
			rep.Error = err.Error()
			emitted[sl.idx] = nil
			reports[sl.idx] = rep
			continue
		}

		// Builder-side trim fallback: only when a positive budget is in
		// effect AND the source overspent.
		if in.Budget > 0 && rep.Tokens > fin.Budget {
			msgs, rep = b.trimByTokens(msgs, rep, fin.Budget)
		}

		emitted[sl.idx] = msgs
		reports[sl.idx] = rep
		droppedTotal += rep.DroppedN

		if in.Budget > 0 {
			remaining -= rep.Tokens
			if remaining < 0 {
				remaining = 0
			}
		}
	}

	// Stitch the final message list in declaration order.
	totalCount := 0
	totalTokens := 0
	for i := range emitted {
		totalCount += len(emitted[i])
		totalTokens += reports[i].Tokens
	}

	out := make([]aimodel.Message, 0, totalCount)
	for _, slc := range emitted {
		out = append(out, slc...)
	}

	report := BuildReport{
		BuilderName:  b.name,
		Strategy:     StrategyOrderedGreedy,
		InputBudget:  in.Budget,
		OutputCount:  totalCount,
		OutputTokens: totalTokens,
		DroppedCount: droppedTotal,
		Sources:      reports,
		Duration:     time.Since(start).Milliseconds(),
	}

	if b.hooks != nil {
		b.hooks.Dispatch(ctx, schema.NewEvent(
			schema.EventContextBuilt,
			in.AgentID,
			in.SessionID,
			report.ToEventData(),
		))
	}

	return BuildResult{Messages: out, Report: report}, nil
}

// runSource calls a single Source.Fetch with light defensive normalisation:
// it backfills Source name and Status="ok" on the report when the source
// left them empty, and recomputes Tokens via the configured estimator when
// the source did not provide one.
func (b *DefaultBuilder) runSource(ctx context.Context, s Source, in FetchInput) ([]aimodel.Message, schema.ContextSourceReport, error) {
	res, err := s.Fetch(ctx, in)
	rep := res.Report

	if rep.Source == "" {
		rep.Source = s.Name()
	}

	if err != nil {
		return nil, rep, err
	}

	// Backfill Status: "skipped" when there is no output; "ok" otherwise.
	if rep.Status == "" {
		if len(res.Messages) == 0 {
			rep.Status = StatusSkipped
		} else {
			rep.Status = StatusOK
		}
	}

	// Backfill OutputN.
	if rep.OutputN == 0 {
		rep.OutputN = len(res.Messages)
	}

	// Backfill Tokens via the builder's estimator when the source did not
	// account for them. Sources that compute tokens themselves keep their
	// own number (they may know better than the heuristic).
	if rep.Tokens == 0 && len(res.Messages) > 0 {
		rep.Tokens = b.sumTokens(res.Messages)
	}

	return res.Messages, rep, nil
}

// sumTokens estimates the total tokens of a message slice using the
// builder's configured estimator. nil-safe.
func (b *DefaultBuilder) sumTokens(ms []aimodel.Message) int {
	est := b.estimator
	if est == nil {
		est = memory.DefaultTokenEstimator
	}

	total := 0
	for _, m := range ms {
		// memory.TokenEstimator works on schema.Message, so wrap.
		total += est(schema.FromAIModelMessage(m))
	}
	return total
}

// trimByTokens drops messages from the front of the slice (oldest first)
// until the running token total fits within budget. The report is updated
// with the new OutputN / Tokens / DroppedN and Status="truncated". budget
// is assumed to be > 0 (callers must guard).
func (b *DefaultBuilder) trimByTokens(ms []aimodel.Message, rep schema.ContextSourceReport, budget int) ([]aimodel.Message, schema.ContextSourceReport) {
	if budget <= 0 || len(ms) == 0 {
		return ms, rep
	}

	// Compute per-message tokens once; keep Tokens consistent with the
	// estimator the builder uses.
	tokens := make([]int, len(ms))
	total := 0
	for i, m := range ms {
		tokens[i] = b.sumTokens([]aimodel.Message{m})
		total += tokens[i]
	}

	// Drop from the head until total <= budget.
	drop := 0
	for drop < len(ms) && total > budget {
		total -= tokens[drop]
		drop++
	}

	if drop == 0 {
		return ms, rep
	}

	out := ms[drop:]
	rep.OutputN = len(out)
	rep.Tokens = total
	rep.DroppedN = (rep.DroppedN) + drop
	rep.Status = StatusTruncated
	return out, rep
}
