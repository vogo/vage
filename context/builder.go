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
	"fmt"
	"log/slog"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// DefaultBuilderName is the BuilderName written into BuildReport.BuilderName
// unless overridden by WithName.
const DefaultBuilderName = "default"

// ErrFixedContentExceedsBudget is returned when must-include messages plus
// reserved output/tools/system already overflow the model window.
var ErrFixedContentExceedsBudget = errors.New("vctx: fixed context content exceeds model window")

type sourceSlot struct {
	idx  int
	src  Source
	must bool
}

// DefaultBuilder is the standard Builder implementation. It runs sources in
// declared order, charges must-include sources first, then shares the
// remaining history budget across optional sources and trims globally.
type DefaultBuilder struct {
	name      string
	sources   []Source
	estimator memory.TokenEstimator
	hooks     *hook.Manager
	// reportSink, when non-nil, persists the BuildReport produced by
	// each Build call. nil disables the per-turn archive — the
	// EventContextBuilt event is still dispatched so live observers
	// keep working.
	reportSink BuildReportSink
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

// WithBuildReportSink installs a per-turn report archive. The Builder
// invokes sink.Save inline after each successful Build; Save errors
// are slog.Warn'd and dropped — observability must not abort an
// in-flight LLM call. nil disables persistence (the default).
func WithBuildReportSink(sink BuildReportSink) Option {
	return func(b *DefaultBuilder) { b.reportSink = sink }
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

// Build runs each source in declaration order, applies the unified budget,
// dispatches EventContextBuilt, and returns the assembled message list
// with the audit report.
//
// Source errors are fail-open: a slog.Warn is emitted, the source is
// recorded with Status="error", and execution continues. The exception is
// SystemPromptSource, which returns a fatal error if its template fails to
// render — that bubbles up as Build's error. Fixed content that overflows
// a bounded window is also fail-closed.
func (b *DefaultBuilder) Build(ctx context.Context, in BuildInput) (BuildResult, error) {
	if err := ctx.Err(); err != nil {
		return BuildResult{}, err
	}

	start := time.Now()

	budget, err := b.resolveBudget(in)
	if err != nil {
		return BuildResult{}, err
	}
	est := budget.EstimatorOrDefault()

	slots := make([]sourceSlot, len(b.sources))
	for i, s := range b.sources {
		slots[i] = sourceSlot{idx: i, src: s, must: isMustInclude(s)}
	}

	emitted := make([][]schema.Message, len(b.sources))
	reports := make([]schema.ContextSourceReport, len(b.sources))

	mustTokens := 0
	systemTokens := 0

	for _, sl := range slots {
		if !sl.must {
			continue
		}

		fin := fromBuildInput(in)
		fin.Budget = 0
		fin.ContextBudget = budget

		msgs, rep, fetchErr := b.runSourceWith(ctx, sl.src, fin, est)
		if fetchErr != nil {
			return BuildResult{}, fetchErr
		}

		emitted[sl.idx] = msgs
		reports[sl.idx] = rep
		mustTokens += rep.Tokens
		if sl.src.Name() == SourceNameSystemPrompt {
			systemTokens = rep.Tokens
		}
	}

	reservedSystem := max(systemTokens, budget.ReservedSystem)
	otherMust := max(mustTokens-systemTokens, 0)
	fixed := reservedSystem + budget.ReservedTools + budget.ReservedOutput + otherMust

	if !budget.Unlimited() && fixed > budget.ModelContextTokens {
		return BuildResult{}, fmt.Errorf("%w: fixed=%d window=%d",
			ErrFixedContentExceedsBudget, fixed, budget.ModelContextTokens)
	}

	if !budget.Unlimited() {
		budget.AvailableHistory = max(budget.ModelContextTokens-fixed, 0)
	}

	for _, sl := range slots {
		if sl.must {
			continue
		}

		if err := ctx.Err(); err != nil {
			return BuildResult{}, err
		}

		fin := fromBuildInput(in)
		fin.ContextBudget = budget
		if budget.Unlimited() {
			fin.Budget = 0
		} else {
			fin.Budget = budget.AvailableHistory
		}

		msgs, rep, fetchErr := b.runSourceWith(ctx, sl.src, fin, est)
		if fetchErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return BuildResult{}, ctxErr
			}

			slog.Warn("vctx: source error",
				"builder", b.name, "source", sl.src.Name(), "error", fetchErr)
			rep.Source = sl.src.Name()
			rep.Status = StatusError
			rep.Error = fetchErr.Error()
			emitted[sl.idx] = nil
			reports[sl.idx] = rep
			continue
		}

		emitted[sl.idx] = msgs
		reports[sl.idx] = rep
	}

	if !budget.Unlimited() {
		emitted, reports = b.trimOptionalToHistory(emitted, reports, slots, budget.AvailableHistory, est)
	}

	out := make([]schema.Message, 0)
	for _, slc := range emitted {
		out = append(out, slc...)
	}

	droppedTotal := 0
	for i := range reports {
		droppedTotal += reports[i].DroppedN
	}

	report := BuildReport{
		BuilderName:      b.name,
		Strategy:         StrategyOrderedGreedy,
		InputBudget:      budget.ModelContextTokens,
		ReservedOutput:   budget.ReservedOutput,
		ReservedTools:    budget.ReservedTools,
		ReservedSystem:   reservedSystem,
		AvailableHistory: budget.AvailableHistory,
		OutputCount:      len(out),
		OutputTokens:     memory.EstimateMessages(est, out),
		DroppedCount:     droppedTotal,
		Sources:          reports,
		Duration:         time.Since(start).Milliseconds(),
	}

	if b.hooks != nil {
		b.hooks.Dispatch(ctx, schema.NewEvent(
			schema.EventContextBuilt,
			in.AgentID,
			in.SessionID,
			report.ToEventData(),
		))
	}

	if b.reportSink != nil && in.SessionID != "" {
		if err := b.reportSink.Save(ctx, in.SessionID, report); err != nil {
			slog.Warn("vctx: persist build_report",
				"error", err,
				"session_id", in.SessionID,
				"agent_id", in.AgentID)
		}
	}

	return BuildResult{Messages: out, Report: report}, nil
}

func (b *DefaultBuilder) resolveBudget(in BuildInput) (memory.Budget, error) {
	var budget memory.Budget
	switch {
	case in.ContextBudget != nil:
		budget = *in.ContextBudget
	case in.Budget > 0:
		budget = memory.Budget{ModelContextTokens: in.Budget}
	case in.Budget < 0:
		return memory.Budget{}, fmt.Errorf("vctx: budget must not be negative")
	}
	if err := budget.Validate(); err != nil {
		return memory.Budget{}, err
	}
	if budget.Estimator == nil {
		budget.Estimator = b.estimator
		if budget.Estimator == nil {
			budget.Estimator = memory.DefaultTokenEstimator
		}
	}
	return budget, nil
}

// runSourceWith calls a single Source.Fetch with defensive normalisation.
func (b *DefaultBuilder) runSourceWith(ctx context.Context, s Source, in FetchInput, est memory.TokenEstimator) ([]schema.Message, schema.ContextSourceReport, error) {
	res, err := s.Fetch(ctx, in)
	rep := res.Report

	if rep.Source == "" {
		rep.Source = s.Name()
	}

	if err != nil {
		return nil, rep, err
	}

	if rep.Status == "" {
		if len(res.Messages) == 0 {
			rep.Status = StatusSkipped
		} else {
			rep.Status = StatusOK
		}
	}

	if rep.OutputN == 0 {
		rep.OutputN = len(res.Messages)
	}

	if rep.Tokens == 0 && len(res.Messages) > 0 {
		rep.Tokens = memory.EstimateMessages(est, res.Messages)
	}

	return res.Messages, rep, nil
}

// trimOptionalToHistory drops optional-source messages from the head of the
// merged declaration order until they fit AvailableHistory. Must-include
// messages are never dropped. Source notes such as workspace_tail_keep are
// preserved when a later global drop removes the remainder.
func (b *DefaultBuilder) trimOptionalToHistory(
	emitted [][]schema.Message,
	reports []schema.ContextSourceReport,
	slots []sourceSlot,
	available int,
	est memory.TokenEstimator,
) ([][]schema.Message, []schema.ContextSourceReport) {
	type ref struct {
		slot int
		msg  schema.Message
		tok  int
	}

	var droppable []ref
	total := 0
	for _, sl := range slots {
		if sl.must {
			continue
		}
		for _, m := range emitted[sl.idx] {
			tok := est(m)
			droppable = append(droppable, ref{slot: sl.idx, msg: m, tok: tok})
			total += tok
		}
	}

	drop := 0
	for drop < len(droppable) && total > available {
		total -= droppable[drop].tok
		drop++
	}
	if drop == 0 {
		return emitted, reports
	}

	keptPerSlot := make([][]schema.Message, len(emitted))
	tokensPerSlot := make([]int, len(emitted))
	origLen := make([]int, len(emitted))
	for i, msgs := range emitted {
		origLen[i] = len(msgs)
	}
	for i := drop; i < len(droppable); i++ {
		r := droppable[i]
		keptPerSlot[r.slot] = append(keptPerSlot[r.slot], r.msg)
		tokensPerSlot[r.slot] += r.tok
	}

	for _, sl := range slots {
		if sl.must {
			continue
		}
		kept := keptPerSlot[sl.idx]
		droppedN := origLen[sl.idx] - len(kept)
		if droppedN <= 0 {
			continue
		}
		emitted[sl.idx] = kept
		rep := reports[sl.idx]
		rep.DroppedN += droppedN
		rep.OutputN = len(kept)
		rep.Tokens = tokensPerSlot[sl.idx]
		if rep.Status != StatusError {
			rep.Status = StatusTruncated
		}
		reports[sl.idx] = rep
	}

	return emitted, reports
}
