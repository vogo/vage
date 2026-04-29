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
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/prompt"
)

// errPrompt is a PromptTemplate whose Render always returns the configured
// error. Used to exercise SystemPromptSource fail-closed behaviour.
type errPrompt struct{ err error }

func (e *errPrompt) Render(_ context.Context, _ map[string]any) (string, error) {
	return "", e.err
}
func (e *errPrompt) Name() string    { return "err" }
func (e *errPrompt) Version() string { return "1" }

// TestSystemPromptSource_Render verifies a non-empty render produces a
// single system message with the rendered text.
func TestSystemPromptSource_Render(t *testing.T) {
	src := &SystemPromptSource{Template: prompt.StringPrompt("you are a helper")}

	res, err := src.Fetch(context.Background(), FetchInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	if res.Messages[0].Role != aimodel.RoleSystem {
		t.Errorf("Role = %q, want system", res.Messages[0].Role)
	}
	if res.Messages[0].Content.Text() != "you are a helper" {
		t.Errorf("text mismatch: %q", res.Messages[0].Content.Text())
	}
	if res.Report.Status != StatusOK {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusOK)
	}
	if !src.MustInclude() {
		t.Errorf("SystemPromptSource MustInclude = false, want true")
	}
}

// TestSystemPromptSource_NilTemplate verifies a nil template is a skip
// (Status = "skipped"), not an error.
func TestSystemPromptSource_NilTemplate(t *testing.T) {
	src := &SystemPromptSource{}
	res, err := src.Fetch(context.Background(), FetchInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestSystemPromptSource_EmptyRender verifies a rendered empty string is
// a skip; no system message produced.
func TestSystemPromptSource_EmptyRender(t *testing.T) {
	src := &SystemPromptSource{Template: prompt.StringPrompt("")}
	res, err := src.Fetch(context.Background(), FetchInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestSystemPromptSource_RenderError verifies a render error propagates
// (fail-closed for infrastructure-level config issues).
func TestSystemPromptSource_RenderError(t *testing.T) {
	src := &SystemPromptSource{Template: &errPrompt{err: errors.New("template parse")}}
	_, err := src.Fetch(context.Background(), FetchInput{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
