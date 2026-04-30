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

// Package vctx (import path "github.com/vogo/vage/context") provides an
// explicit Builder/Source abstraction for assembling LLM prompts from a
// Session. It separates "what facts exist" (Session, memory, state stores)
// from "what messages to send to the LLM" (the Builder's output), so the
// assembly process is pluggable and auditable.
//
// The package is named "vctx" to avoid colliding with the standard library
// "context" package; the import path is github.com/vogo/vage/context.
package vctx

import (
	"context"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// Source-name constants provide a single source of truth for hook payloads,
// TaskAgent integration (e.g. looking up OriginalCount from a SessionMemory
// report), and test assertions. External Source implementations are free to
// pick their own names.
const (
	SourceNameSystemPrompt    = "system_prompt"
	SourceNameSessionMemory   = "session_memory"
	SourceNameSessionState    = "session_state"
	SourceNameWorkspace       = "workspace"
	SourceNameVectorRecall    = "vector_recall"
	SourceNameSessionTree     = "session_tree"
	SourceNameRequestMessages = "request_messages"
)

// Status constants for ContextSourceReport.Status.
const (
	StatusOK        = "ok"
	StatusSkipped   = "skipped"
	StatusError     = "error"
	StatusTruncated = "truncated"
)

// Strategy constants for BuildReport.Strategy.
const (
	StrategyOrderedGreedy = "ordered_greedy"
)

// Source is the pluggable plugin a Builder calls to fetch one slice of
// messages. Implementations must be safe for concurrent use across distinct
// Builder.Build invocations; a single Build call invokes its sources
// sequentially.
type Source interface {
	// Name returns a stable identifier used in reports and hook payloads.
	Name() string

	// Fetch produces the messages this source contributes for the given
	// FetchInput. Errors are fail-open by convention: the Builder logs a
	// warning, marks Status="error" in the report, and continues with the
	// next source. SystemPromptSource is the documented exception (see
	// design §7).
	Fetch(ctx context.Context, in FetchInput) (FetchResult, error)
}

// MustIncludeSource is an optional extension. Sources that return true from
// MustInclude are treated as required by the Builder: their token cost is
// charged before any optional source receives budget, and the Builder's
// trim-by-token fallback never drops their messages.
//
// SystemPromptSource and RequestMessagesSource implement this interface.
type MustIncludeSource interface {
	MustInclude() bool
}

// FetchInput is the input handed to Source.Fetch.
//
// SessionID is required; the v1 built-in sources query stores by id rather
// than holding a *session.Session pointer, which keeps the contract narrow.
// If a future source needs metadata, it can resolve via a SessionMetaStore
// passed at construction time.
type FetchInput struct {
	SessionID string
	AgentID   string
	Intent    string             // optional tag, e.g. "react-iter"
	Request   *schema.RunRequest // current-turn request (may be nil)
	// Budget is the token allowance for this source's output. 0 means
	// unlimited; > 0 is a soft hint — the source may emit more, in which
	// case the Builder applies its fallback trim.
	Budget int
	Vars   map[string]any
}

// FetchResult is the value Source.Fetch returns.
type FetchResult struct {
	Messages []aimodel.Message
	Report   schema.ContextSourceReport
}

// Builder assembles a Session and a current-turn Request into the message
// sequence sent to the LLM, and produces a BuildReport describing the
// assembly for audit.
type Builder interface {
	Build(ctx context.Context, in BuildInput) (BuildResult, error)
	Name() string
}

// BuildInput is the input handed to Builder.Build.
//
// Budget is the total token allowance for the whole prompt. 0 means
// unlimited; the must-include sources (system + request) are always
// charged first regardless of Budget, then the remainder is doled out to
// optional sources in declaration order.
type BuildInput struct {
	SessionID string
	AgentID   string
	Intent    string
	Request   *schema.RunRequest
	Budget    int
	Vars      map[string]any
}

// BuildResult is the value Builder.Build returns.
type BuildResult struct {
	Messages []aimodel.Message
	Report   BuildReport
}

// fromBuildInput projects a BuildInput onto a FetchInput. Budget is left at
// zero so the caller can fill it per-source.
func fromBuildInput(in BuildInput) FetchInput {
	return FetchInput{
		SessionID: in.SessionID,
		AgentID:   in.AgentID,
		Intent:    in.Intent,
		Request:   in.Request,
		Vars:      in.Vars,
	}
}

// isMustInclude reports whether s is a MustIncludeSource that returns true.
func isMustInclude(s Source) bool {
	mi, ok := s.(MustIncludeSource)
	return ok && mi.MustInclude()
}
