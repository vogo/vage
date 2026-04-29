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

package context_tests

import (
	"context"
	"strings"
	"testing"

	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/schema"
)

// TestBuilder_SystemPromptRenderError_FailClosed exercises design §7 +
// AC-4.2's documented exception: SystemPromptSource is fail-closed. A
// template error must propagate as Build's error rather than being
// quietly recorded in the report.
func TestBuilder_SystemPromptRenderError_FailClosed(t *testing.T) {
	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: errPromptTemplate{}}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(context.Background(), vctx.BuildInput{
		SessionID: "failclosed-session",
		Request: &schema.RunRequest{
			SessionID: "failclosed-session",
			Messages:  []schema.Message{schema.NewUserMessage("hi")},
		},
	})
	if err == nil {
		t.Fatalf("Build returned nil error; expected fail-closed: %+v", res)
	}
	if !strings.Contains(err.Error(), "render system prompt") {
		t.Errorf("error = %q, want it to mention 'render system prompt'", err.Error())
	}
	if res.Messages != nil {
		t.Errorf("res.Messages = %v, want nil on error", res.Messages)
	}
}

// TestBuilder_OptionalSourceError_FailOpen complements the test above
// (AC-4.2): an optional Source returning an error must NOT abort the
// Build. Subsequent sources still run, and the failed source is
// recorded with Status="error" in the report.
func TestBuilder_OptionalSourceError_FailOpen(t *testing.T) {
	failing := &failingSource{name: "failing_optional"}

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(failing),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(context.Background(), vctx.BuildInput{
		SessionID: "failopen-session",
		Request: &schema.RunRequest{
			SessionID: "failopen-session",
			Messages:  []schema.Message{schema.NewUserMessage("hi")},
		},
	})
	if err != nil {
		t.Fatalf("Build returned err = %v; expected fail-open", err)
	}

	// The request message must still have been emitted by the must-include
	// RequestMessagesSource.
	if len(res.Messages) != 1 || res.Messages[0].Content.Text() != "hi" {
		t.Errorf("messages = %+v, want [user 'hi']", res.Messages)
	}

	// The failing source's report must show Status="error".
	var found bool
	for _, s := range res.Report.Sources {
		if s.Source == "failing_optional" {
			found = true
			if s.Status != vctx.StatusError {
				t.Errorf("failing source Status = %q, want %q", s.Status, vctx.StatusError)
			}
			if s.Error == "" {
				t.Errorf("failing source Error is empty, want non-empty")
			}
		}
	}
	if !found {
		t.Errorf("failing source missing from BuildReport.Sources")
	}
}

// failingSource is a non-must-include Source that always errors. It
// drives the fail-open path (Pass 2 in DefaultBuilder.Build).
type failingSource struct{ name string }

func (s *failingSource) Name() string { return s.name }
func (s *failingSource) Fetch(_ context.Context, _ vctx.FetchInput) (vctx.FetchResult, error) {
	return vctx.FetchResult{Report: schema.ContextSourceReport{Source: s.name}},
		errFakeOptional
}

// errFakeOptional is a sentinel error reused by failingSource.
var errFakeOptional = errFakeOptionalErr{}

type errFakeOptionalErr struct{}

func (errFakeOptionalErr) Error() string { return "fake optional source failure" }
