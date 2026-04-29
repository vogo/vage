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

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// SystemPromptSource renders a prompt.PromptTemplate into a single
// system-role aimodel.Message. It is a MustInclude source: the system
// prompt is infrastructural and is never trimmed by budget.
//
// Render failures are fail-closed by design — they signal an agent
// configuration bug and surface to the Builder caller.
type SystemPromptSource struct {
	Template prompt.PromptTemplate
}

// Compile-time interface conformance.
var (
	_ Source            = (*SystemPromptSource)(nil)
	_ MustIncludeSource = (*SystemPromptSource)(nil)
)

// Name returns SourceNameSystemPrompt.
func (s *SystemPromptSource) Name() string { return SourceNameSystemPrompt }

// MustInclude reports true: the system prompt is not subject to budget
// trimming.
func (s *SystemPromptSource) MustInclude() bool { return true }

// Fetch renders the template and returns a single system message. nil
// templates and empty render output both produce Status="skipped" with no
// messages.
func (s *SystemPromptSource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameSystemPrompt}

	if s.Template == nil {
		rep.Status = StatusSkipped
		rep.Note = "no template"
		return FetchResult{Report: rep}, nil
	}

	text, err := s.Template.Render(ctx, in.Vars)
	if err != nil {
		// Fail-closed: surface configuration bugs immediately.
		return FetchResult{Report: rep}, fmt.Errorf("vctx: render system prompt: %w", err)
	}

	if text == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty render"
		return FetchResult{Report: rep}, nil
	}

	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}

	rep.Status = StatusOK
	rep.OutputN = 1
	// Tokens left at zero so the Builder fills it with its estimator —
	// keeps token accounting consistent with the rest of the run.

	return FetchResult{Messages: []aimodel.Message{msg}, Report: rep}, nil
}
