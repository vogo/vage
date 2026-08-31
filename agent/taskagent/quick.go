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
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/prompt"
)

// Quick builds an Agent from the identity, endpoint, model and system prompt
// almost every entry-level setup has to supply anyway, collapsing the usual
// agent.Config + WithCaller/WithModel/WithSystemPrompt expansion into a single
// call.
//
// It is a thin wrapper, not a reduced agent: the returned Agent is exactly
// what New produces for the equivalent options, so defaults, protocol
// derivation from the caller, and every runtime path stay identical.
// systemPrompt goes through prompt.StringPrompt, so template interpolation and
// the empty string behave as they would when passing the option by hand.
//
// opts are applied after the three preset options, following the package's
// last-one-wins rule: they add tools, budgets or guards, and may also override
// the preset caller, model or system prompt. Quick validates nothing — a nil
// caller or an empty model fails exactly when the same New call would.
//
// Reach for New directly when the agent needs a description, a named or
// versioned prompt template, or any other Config field.
func Quick(id, name string, caller largemodel.Caller, model, systemPrompt string, opts ...Option) *Agent {
	return New(
		agent.Config{ID: id, Name: name},
		append([]Option{
			WithCaller(caller),
			WithModel(model),
			WithSystemPrompt(prompt.StringPrompt(systemPrompt)),
		}, opts...)...,
	)
}

// QuickValidated is the validated counterpart to Quick: it expands to the
// same NewValidated call the equivalent New call would, so the three preset
// options are applied first and trailing opts win. Unlike Quick it returns
// a construction-time ErrInterruptConfig for a broken interrupt pair (see
// NewValidated); the returned agent is nil on error. Migration from Quick
// is a mechanical signature change — handle the error instead of discarding
// it.
func QuickValidated(id, name string, caller largemodel.Caller, model, systemPrompt string, opts ...Option) (*Agent, error) {
	return NewValidated(
		agent.Config{ID: id, Name: name},
		append([]Option{
			WithCaller(caller),
			WithModel(model),
			WithSystemPrompt(prompt.StringPrompt(systemPrompt)),
		}, opts...)...,
	)
}
