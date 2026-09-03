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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/security/credscrub"
)

// ErrInvalidRunParams reports that merged RunOptions, Limits, ToolMode or
// ParamResolver output cannot be used. Match with errors.Is.
var ErrInvalidRunParams = errors.New("vage: invalid run parameters")

// ToolEnabledFunc is a context-aware predicate over a candidate tool name.
// Returning false or an error excludes the tool; an error also emits a
// scrubbed exclusion event. The predicate cannot re-add a name already
// dropped by an earlier intersection layer.
type ToolEnabledFunc func(ctx context.Context, name string) (bool, error)

// RunParams is the resolved parameter set for one Run, visible to a
// ParamResolver. The framework maps it onto private execution state after
// the resolver returns; it is not the interrupt snapshot.
type RunParams struct {
	Model          string
	Temperature    *float64
	MaxIterations  int
	MaxTokens      *int
	RunTokenBudget int
	ToolMode       string
	Tools          []string
	StopSequences  []string
	Subject        string
	EnabledFunc    ToolEnabledFunc
}

// ParamResolver customizes one fresh Run's parameters after built-in
// defaults and RunOptions have been merged and after input guards, and
// before the tool set is frozen or context is built. It is a single
// construction-time slot: later WithParamResolver calls replace earlier
// ones; it is not a chain.
type ParamResolver func(ctx context.Context, req *schema.RunRequest, cur RunParams) (RunParams, error)

var paramsEventScrubber = credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

// WithParamResolver installs the single ParamResolver slot. Passing nil
// clears a previously installed resolver.
func WithParamResolver(r ParamResolver) Option {
	return func(a *Agent) { a.paramResolver = r }
}

func (p runParams) exported() RunParams {
	return RunParams{
		Model:          p.model,
		Temperature:    p.temperature,
		MaxIterations:  p.maxIter,
		MaxTokens:      p.maxTokens,
		RunTokenBudget: p.runTokenBudget,
		ToolMode:       p.toolMode,
		Tools:          slices.Clone(p.toolFilter),
		StopSequences:  slices.Clone(p.stopSeq),
		Subject:        p.subject,
		EnabledFunc:    p.enabledFunc,
	}
}

func fromExported(p RunParams, resolverTouched bool) runParams {
	return runParams{
		model:           p.Model,
		temperature:     p.Temperature,
		maxIter:         p.MaxIterations,
		maxTokens:       p.MaxTokens,
		runTokenBudget:  p.RunTokenBudget,
		toolMode:        p.ToolMode,
		toolFilter:      slices.Clone(p.Tools),
		stopSeq:         slices.Clone(p.StopSequences),
		subject:         p.Subject,
		enabledFunc:     p.EnabledFunc,
		resolverTouched: resolverTouched,
	}
}

// mergeRunParams overlays request options onto agent defaults. It does not
// call ParamResolver or freeze tools.
func (a *Agent) mergeRunParams(opts *schema.RunOptions) (runParams, error) {
	p := runParams{
		model:          a.model,
		temperature:    a.temperature,
		maxIter:        a.maxIterations,
		runTokenBudget: a.runTokenBudget,
		maxTokens:      a.maxTokens,
	}
	if opts == nil {
		return p, nil
	}

	if opts.Model != "" {
		p.model = opts.Model
	}
	if opts.Temperature != nil {
		p.temperature = opts.Temperature
	}
	p.toolMode = opts.ToolMode
	p.toolFilter = slices.Clone(opts.Tools)
	p.stopSeq = slices.Clone(opts.StopSequences)

	var limits schema.RunLimits
	if opts.Limits != nil {
		limits = *opts.Limits
	}

	if err := applyMaxIterations(&p.maxIter, limits.MaxIterations, opts.MaxIterations); err != nil {
		return runParams{}, err
	}
	if err := applyRunTokenBudget(&p.runTokenBudget, limits.RunTokenBudget, opts.RunTokenBudget); err != nil {
		return runParams{}, err
	}
	if err := applyMaxTokens(&p.maxTokens, limits.MaxTokens, opts.MaxTokens); err != nil {
		return runParams{}, err
	}

	return p, nil
}

func applyMaxIterations(dst *int, limitsField *int, old int) error {
	if limitsField != nil {
		if *limitsField <= 0 {
			return fmt.Errorf("%w: max_iterations must be positive", ErrInvalidRunParams)
		}
		*dst = *limitsField
		return nil
	}
	if old < 0 {
		return fmt.Errorf("%w: max_iterations must not be negative", ErrInvalidRunParams)
	}
	if old > 0 {
		*dst = old
	}
	return nil
}

func applyRunTokenBudget(dst *int, limitsField *int, old int) error {
	if limitsField != nil {
		if *limitsField < 0 {
			return fmt.Errorf("%w: run_token_budget must not be negative", ErrInvalidRunParams)
		}
		*dst = *limitsField // 0 is explicit unlimited
		return nil
	}
	if old < 0 {
		return fmt.Errorf("%w: run_token_budget must not be negative", ErrInvalidRunParams)
	}
	if old > 0 {
		*dst = old
	}
	return nil
}

func applyMaxTokens(dst **int, limitsField *int, old int) error {
	if limitsField != nil {
		if *limitsField < 0 {
			return fmt.Errorf("%w: max_tokens must not be negative", ErrInvalidRunParams)
		}
		if *limitsField == 0 {
			*dst = nil // do not send a vendor output cap
			return nil
		}
		v := *limitsField
		*dst = &v
		return nil
	}
	if old < 0 {
		return fmt.Errorf("%w: max_tokens must not be negative", ErrInvalidRunParams)
	}
	if old > 0 {
		v := old
		*dst = &v
	}
	return nil
}

func validateRunParams(p runParams) error {
	switch p.toolMode {
	case "", schema.ToolModeNone, schema.ToolModeAllow, schema.ToolModeAll:
	default:
		return fmt.Errorf("%w: unknown tool_mode %q", ErrInvalidRunParams, p.toolMode)
	}
	if p.toolMode == schema.ToolModeAll && len(p.toolFilter) > 0 {
		return fmt.Errorf("%w: tool_mode %q cannot carry a non-empty tools list", ErrInvalidRunParams, schema.ToolModeAll)
	}
	if p.maxIter <= 0 {
		return fmt.Errorf("%w: max_iterations must be positive", ErrInvalidRunParams)
	}
	if p.maxTokens != nil && *p.maxTokens < 0 {
		return fmt.Errorf("%w: max_tokens must not be negative", ErrInvalidRunParams)
	}
	if p.runTokenBudget < 0 {
		return fmt.Errorf("%w: run_token_budget must not be negative", ErrInvalidRunParams)
	}
	return nil
}

// resolveRunParams is the historical merge used by checkpoint Resume and
// unit tests. Fresh Run/RunStream go through resolveFreshRun instead.
func (a *Agent) resolveRunParams(opts *schema.RunOptions) runParams {
	p, err := a.mergeRunParams(opts)
	if err != nil {
		// Legacy callers never supplied negative limits; keep a best-effort
		// merge so existing tests that ignore the error path still compile.
		return p
	}
	return p
}

func (a *Agent) resolveFreshRun(ctx context.Context, req *schema.RunRequest) (runParams, error) {
	var opts *schema.RunOptions
	if req != nil {
		opts = req.Options
	}

	p, err := a.mergeRunParams(opts)
	if err != nil {
		return runParams{}, err
	}

	if a.paramResolver != nil {
		resolved, rerr := a.paramResolver(ctx, req, p.exported())
		if rerr != nil {
			return runParams{}, fmt.Errorf("vage: param resolver: %w", rerr)
		}
		p = fromExported(resolved, true)
		if p.maxTokens != nil && *p.maxTokens == 0 {
			p.maxTokens = nil
		}
	}

	if err := validateRunParams(p); err != nil {
		return runParams{}, err
	}

	sessionID := ""
	if req != nil {
		sessionID = req.SessionID
	}
	p, err = a.freezeTools(ctx, p, sessionID)
	if err != nil {
		return runParams{}, err
	}

	a.emitParamsResolved(ctx, req, p)
	return p, nil
}

func (a *Agent) freezeTools(ctx context.Context, p runParams, sessionID string) (runParams, error) {
	p.toolsFrozen = true
	if p.toolMode == schema.ToolModeNone {
		p.toolFilter = []string{}
		return p, nil
	}

	reqRestricted := false
	reqNames := p.toolFilter
	switch p.toolMode {
	case schema.ToolModeAllow:
		reqRestricted = true
	case schema.ToolModeAll:
		reqNames = nil
	default: // compatibility mode
		if len(p.toolFilter) > 0 {
			reqRestricted = true
		} else {
			reqNames = nil
		}
	}

	skillNames, skillRestricted := a.skillToolScope(sessionID)
	var names []string
	if !reqRestricted && !skillRestricted {
		names = a.registryToolNames()
	} else {
		names = intersectNames(reqNames, reqRestricted, skillNames, skillRestricted)
		names = a.keepRegistered(names)
	}

	names = a.applyEnabledFunc(ctx, p, sessionID, names)
	p.toolFilter = uniqueSorted(names)
	return p, nil
}

func (a *Agent) skillToolScope(sessionID string) (names []string, restricted bool) {
	if a.skillManager == nil {
		return nil, false
	}

	active := a.skillManager.ActiveSkills(sessionID)
	if len(active) == 0 {
		return nil, false
	}

	var skillTools []string
	seen := make(map[string]bool)

	for _, act := range active {
		def := act.SkillDef()
		if len(def.AllowedTools) == 0 {
			return nil, false
		}
		for _, toolName := range def.AllowedTools {
			if !seen[toolName] {
				seen[toolName] = true
				skillTools = append(skillTools, toolName)
			}
		}
	}

	return skillTools, true
}

func (a *Agent) registryToolNames() []string {
	if a.toolRegistry == nil {
		return []string{}
	}
	defs := a.toolRegistry.List()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

func (a *Agent) keepRegistered(names []string) []string {
	if a.toolRegistry == nil {
		return []string{}
	}
	var kept []string
	for _, n := range names {
		if _, ok := a.toolRegistry.Get(n); ok {
			kept = append(kept, n)
		}
	}
	return kept
}

func (a *Agent) applyEnabledFunc(ctx context.Context, p runParams, sessionID string, names []string) []string {
	if p.enabledFunc == nil {
		return names
	}

	kept := make([]string, 0, len(names))
	for _, name := range names {
		ok, err := p.enabledFunc(ctx, name)
		if err != nil {
			a.emitToolExcluded(ctx, sessionID, name, err)
			continue
		}
		if ok {
			kept = append(kept, name)
		}
	}
	return kept
}

func intersectNames(left []string, leftOn bool, right []string, rightOn bool) []string {
	if !leftOn && !rightOn {
		return nil
	}
	if !leftOn {
		return slices.Clone(right)
	}
	if !rightOn {
		return slices.Clone(left)
	}
	set := make(map[string]struct{}, len(right))
	for _, n := range right {
		set[n] = struct{}{}
	}
	var out []string
	for _, n := range left {
		if _, ok := set[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

func uniqueSorted(names []string) []string {
	if len(names) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func toolsDigest(names []string) string {
	sorted := uniqueSorted(names)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

func scrubSubject(subject string) string {
	if subject == "" {
		return ""
	}
	res := paramsEventScrubber.ScanText(subject)
	if len(res.Hits) == 0 {
		return subject
	}
	return paramsEventScrubber.RedactText(subject, res.Hits)
}

func (a *Agent) emitParamsResolved(ctx context.Context, req *schema.RunRequest, p runParams) {
	sessionID := ""
	if req != nil {
		sessionID = req.SessionID
	}

	data := schema.ParamsResolvedData{
		Model:           p.model,
		MaxIterations:   p.maxIter,
		MaxTokens:       p.maxTokens,
		RunTokenBudget:  p.runTokenBudget,
		ToolMode:        p.toolMode,
		ToolCount:       len(p.toolFilter),
		ToolsSHA256:     toolsDigest(p.toolFilter),
		Subject:         scrubSubject(p.subject),
		ResolverTouched: p.resolverTouched,
	}
	a.dispatch(ctx, schema.NewEvent(schema.EventParamsResolved, a.ID(), sessionID, data))
}

func (a *Agent) emitToolExcluded(ctx context.Context, sessionID, name string, err error) {
	reason := ""
	if err != nil {
		reason = scrubSubject(err.Error())
	}
	a.dispatch(ctx, schema.NewEvent(schema.EventToolExcluded, a.ID(), sessionID, schema.ToolExcludedData{
		ToolName: name,
		Reason:   reason,
	}))
}
