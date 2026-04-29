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

// Package context_tests holds integration tests for the vage/context
// (vctx) Builder/Source abstraction. The tests boot real packages
// (memory.Manager, hook.Manager, session.MapSessionStore, the actual
// Builder + every built-in Source) and only mock the LLM.
package context_tests //nolint:revive // integration test package

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// fakeChatCompleter is a minimal aimodel.ChatCompleter used to capture the
// message list TaskAgent forwards to the LLM. Each invocation pops one
// pre-configured response and stores the request that produced it so tests
// can assert on the exact prompt assembly.
type fakeChatCompleter struct {
	mu        sync.Mutex
	calls     int
	requests  []*aimodel.ChatRequest
	responses []*aimodel.ChatResponse
}

func (m *fakeChatCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Snapshot the request — the slice may be reused/extended by the agent
	// after this call returns, so we keep a defensive copy of Messages.
	cloned := *req
	cloned.Messages = append([]aimodel.Message(nil), req.Messages...)
	m.requests = append(m.requests, &cloned)

	if m.calls >= len(m.responses) {
		return nil, errors.New("fake: no more responses")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func (m *fakeChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("fake: stream not implemented")
}

func (m *fakeChatCompleter) firstRequest(t *testing.T) *aimodel.ChatRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		t.Fatalf("fake: no chat requests captured")
	}
	return m.requests[0]
}

// stopResponse builds a ChatResponse whose finish reason terminates the
// ReAct loop on the first iteration — exactly what we need to capture the
// initial-prompt message slice and bail out.
func stopResponse(text string) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}

// recordingHook collects every dispatched event so tests can fish out
// EventContextBuilt and inspect its payload.
type recordingHook struct {
	mu     sync.Mutex
	events []schema.Event
}

func newRecordingHook() *recordingHook { return &recordingHook{} }

func (h *recordingHook) OnEvent(_ context.Context, e schema.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}

func (h *recordingHook) Filter() []string { return nil }

func (h *recordingHook) byType(t string) []schema.Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []schema.Event
	for _, e := range h.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// installHook returns a hook.Manager with the given recordingHook registered.
func installHook(h *recordingHook) *hook.Manager {
	mgr := hook.NewManager()
	mgr.Register(h)
	return mgr
}

// errPromptTemplate is a PromptTemplate whose Render always returns an
// error. It exercises the SystemPromptSource fail-closed path documented
// in design §7.
type errPromptTemplate struct{}

func (errPromptTemplate) Render(_ context.Context, _ map[string]any) (string, error) {
	return "", errors.New("fake render failure")
}

func (errPromptTemplate) Name() string    { return "err-template" }
func (errPromptTemplate) Version() string { return "1" }

// Compile-time conformance check.
var _ prompt.PromptTemplate = errPromptTemplate{}
