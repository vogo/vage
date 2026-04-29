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

	"github.com/vogo/vage/schema"
)

// RequestMessagesSource emits the messages carried in BuildInput.Request.
// It is the last "must-include" source in a typical Builder pipeline:
// without the current-turn user input the LLM has nothing to answer.
type RequestMessagesSource struct{}

// Compile-time interface conformance.
var (
	_ Source            = (*RequestMessagesSource)(nil)
	_ MustIncludeSource = (*RequestMessagesSource)(nil)
)

// Name returns SourceNameRequestMessages.
func (s *RequestMessagesSource) Name() string { return SourceNameRequestMessages }

// MustInclude reports true: the current-turn request is never trimmed.
func (s *RequestMessagesSource) MustInclude() bool { return true }

// Fetch unwraps Request.Messages into aimodel.Message form. nil Request
// or empty Messages produce Status="skipped".
func (s *RequestMessagesSource) Fetch(_ context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameRequestMessages}

	if in.Request == nil || len(in.Request.Messages) == 0 {
		rep.Status = StatusSkipped
		return FetchResult{Report: rep}, nil
	}

	out := schema.ToAIModelMessages(in.Request.Messages)
	rep.Status = StatusOK
	rep.InputN = len(out)
	rep.OutputN = len(out)

	return FetchResult{Messages: out, Report: rep}, nil
}
