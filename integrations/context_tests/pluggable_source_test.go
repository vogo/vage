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

	"github.com/vogo/aimodel"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// extraSystemSource is a custom Source used to verify pluggability — its
// output messages must show up in the Builder result, in declaration
// order. It exercises AC-1.3 / AC-4.1 (Source interface stable enough to
// add new sources without changing Builder).
type extraSystemSource struct {
	name string
	text string
}

func (s *extraSystemSource) Name() string { return s.name }

func (s *extraSystemSource) Fetch(_ context.Context, _ vctx.FetchInput) (vctx.FetchResult, error) {
	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(s.text),
	}
	return vctx.FetchResult{
		Messages: []aimodel.Message{msg},
		Report: schema.ContextSourceReport{
			Source:  s.name,
			Status:  vctx.StatusOK,
			InputN:  1,
			OutputN: 1,
		},
	}, nil
}

// TestBuilder_PluggableSource_AppearsInOutput verifies that a custom
// Source's messages appear in the final Builder output between the
// declared neighbours, with the correct ordering and a corresponding
// FetchReport (AC-1.3, AC-4.1).
func TestBuilder_PluggableSource_AppearsInOutput(t *testing.T) {
	custom := &extraSystemSource{name: "custom_marker", text: "<<INJECTED>>"}

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("base sys")}),
		vctx.WithSource(custom),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(context.Background(), vctx.BuildInput{
		SessionID: "plug-session",
		AgentID:   "plug-agent",
		Request: &schema.RunRequest{
			SessionID: "plug-session",
			Messages:  []schema.Message{schema.NewUserMessage("user q")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(res.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(res.Messages))
	}

	// Order: system, custom (system role), user.
	if res.Messages[0].Content.Text() != "base sys" {
		t.Errorf("messages[0] = %q, want %q", res.Messages[0].Content.Text(), "base sys")
	}
	if res.Messages[1].Content.Text() != "<<INJECTED>>" {
		t.Errorf("messages[1] = %q, want %q", res.Messages[1].Content.Text(), "<<INJECTED>>")
	}
	if res.Messages[2].Content.Text() != "user q" {
		t.Errorf("messages[2] = %q, want %q", res.Messages[2].Content.Text(), "user q")
	}

	// The custom source's report must have been recorded by the builder.
	var found bool
	for _, s := range res.Report.Sources {
		if s.Source == "custom_marker" {
			found = true
			if s.Status != vctx.StatusOK {
				t.Errorf("custom source Status = %q, want %q", s.Status, vctx.StatusOK)
			}
			if s.OutputN != 1 {
				t.Errorf("custom source OutputN = %d, want 1", s.OutputN)
			}
		}
	}
	if !found {
		var names []string
		for _, s := range res.Report.Sources {
			names = append(names, s.Source)
		}
		t.Errorf("custom_marker missing from BuildReport.Sources; got: %s",
			strings.Join(names, ","))
	}
}
