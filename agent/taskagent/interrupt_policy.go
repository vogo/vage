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

	"github.com/vogo/largemodel/schema"
	"github.com/vogo/vage/interrupt"
)

// InterruptPolicy decides, for one batch of model-requested tool calls,
// which ones need an external (typically human) decision before any
// handler in the batch runs.
//
// Intercept must not mutate calls, and should be fast and side-effect
// free: it runs synchronously on the ReAct hot path, once per iteration
// that produces tool calls, before any tool executes.
type InterruptPolicy interface {
	// Intercept examines the full assistant tool-call batch and returns
	// the subset of ToolCall.ID that must be decided externally. The
	// result must be unique IDs drawn from calls (the framework rejects
	// duplicates and unknown IDs before persist). A nil or empty result
	// means "do not interrupt": the batch executes normally, exactly as
	// if no InterruptPolicy were configured.
	Intercept(ctx context.Context, sessionID string, calls []schema.ToolCall) []string
}

// InterruptWitness is the optional snapshot a policy can attach to a
// frozen batch. vage stores the snapshot and compares Fingerprint for
// equality; it does not interpret Classification. A policy that does not
// implement InterruptWitness persists an empty fingerprint, and resume
// does not re-check the flag basis.
//
// Witness and Intercept must agree for the same calls: the set of
// assessments with Flagged set must equal the IDs Intercept returns, and
// there must be exactly one assessment per tool call. A mismatch fails
// the Run before Create.
type InterruptWitness interface {
	Witness(ctx context.Context, sessionID string, calls []schema.ToolCall) interrupt.PolicySnapshot
}

// InterruptPolicyFunc adapts a plain function to InterruptPolicy.
type InterruptPolicyFunc func(ctx context.Context, sessionID string, calls []schema.ToolCall) []string

// Intercept calls f.
func (f InterruptPolicyFunc) Intercept(ctx context.Context, sessionID string, calls []schema.ToolCall) []string {
	return f(ctx, sessionID, calls)
}

// interruptPolicyByToolName is the convenience policy behind
// WithInterruptToolNames: it flags every call whose Name is in the
// configured set.
type interruptPolicyByToolName map[string]struct{}

func (p interruptPolicyByToolName) Intercept(_ context.Context, _ string, calls []schema.ToolCall) []string {
	var pending []string
	for _, tc := range calls {
		if _, ok := p[tc.Name]; ok {
			pending = append(pending, tc.ID)
		}
	}
	return pending
}
