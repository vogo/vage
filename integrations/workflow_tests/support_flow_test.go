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

package workflow_tests //nolint:revive // integration test package

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/workflow"
)

type ticket struct {
	Query    string
	Category string
	Reply    string
}

func TestSupportFlowCustomAgentEndToEnd(t *testing.T) {
	query := workflow.NewField(
		"query",
		func(s ticket) string { return s.Query },
		func(s *ticket, v string) { s.Query = v },
	)
	category := workflow.NewField(
		"category",
		func(s ticket) string { return s.Category },
		func(s *ticket, v string) { s.Category = v },
	)
	reply := workflow.NewField(
		"reply",
		func(s ticket) string { return s.Reply },
		func(s *ticket, v string) { s.Reply = v },
	)

	classifier := agent.NewCustomAgent(
		agent.Config{ID: "classifier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			if req.Metadata != nil {
				t.Fatal("classifier request carried Metadata")
			}
			cat := "general"
			if strings.Contains(strings.ToLower(req.Messages[0].Text()), "refund") {
				cat = "billing"
			}
			return &schema.RunResponse{
				Messages: []schema.Message{
					schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, cat),
				},
			}, nil
		},
	)
	replier := agent.NewCustomAgent(
		agent.Config{ID: "replier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			if req.Metadata != nil {
				t.Fatal("replier request carried Metadata")
			}
			text := "Thanks for writing in."
			if req.Messages[0].Text() == "billing" {
				text = "We refunded your charge."
			}
			return &schema.RunResponse{
				Messages: []schema.Message{
					schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, text),
				},
			}, nil
		},
	)

	var _ workflow.Runner = classifier

	wf, err := workflow.New([]workflow.Node[ticket]{
		{
			ID: "classify",
			Run: workflow.AdaptRunner(
				classifier,
				func(snap workflow.Snapshot[ticket]) (*schema.RunRequest, error) {
					return &schema.RunRequest{
						Messages: []schema.Message{
							schema.NewUserMessage(schema.ProtocolOpenAIChat, workflow.Get(snap, query)),
						},
					}, nil
				},
				func(_ workflow.Snapshot[ticket], resp *schema.RunResponse) (workflow.Patch[ticket], error) {
					return workflow.NewPatch(workflow.Set(category, resp.Messages[0].Text())), nil
				},
			),
		},
		{
			ID:   "reply",
			Deps: []string{"classify"},
			Run: workflow.AdaptRunner(
				replier,
				func(snap workflow.Snapshot[ticket]) (*schema.RunRequest, error) {
					return &schema.RunRequest{
						Messages: []schema.Message{
							schema.NewUserMessage(schema.ProtocolOpenAIChat, workflow.Get(snap, category)),
						},
					}, nil
				},
				func(_ workflow.Snapshot[ticket], resp *schema.RunResponse) (workflow.Patch[ticket], error) {
					return workflow.NewPatch(workflow.Set(reply, resp.Messages[0].Text())), nil
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := wf.Run(context.Background(), ticket{Query: "refund please"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Category != "billing" || got.Reply != "We refunded your charge." || got.Query != "refund please" {
		t.Fatalf("got %+v", got)
	}
}
