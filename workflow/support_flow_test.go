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

package workflow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/workflow"
)

func TestSupportFlowEndToEndWithoutMetadata(t *testing.T) {
	var classifierMeta, replierMeta map[string]any

	classifier := agent.NewCustomAgent(
		agent.Config{ID: "classifier", Name: "Classifier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			classifierMeta = req.Metadata
			q := req.Messages[0].Text()
			cat := "general"
			if strings.Contains(strings.ToLower(q), "refund") {
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
		agent.Config{ID: "replier", Name: "Replier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			replierMeta = req.Metadata
			cat := req.Messages[0].Text()
			reply := "Thanks for writing in."
			if cat == "billing" {
				reply = "We refunded your charge."
			}
			return &schema.RunResponse{
				Messages: []schema.Message{
					schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, reply),
				},
			}, nil
		},
	)

	var _ workflow.Runner = classifier
	var _ workflow.Runner = replier

	wf, err := workflow.New([]workflow.Node[Ticket]{
		{
			ID: "classify",
			Run: workflow.AdaptRunner(
				classifier,
				func(snap workflow.Snapshot[Ticket]) (*schema.RunRequest, error) {
					return &schema.RunRequest{
						Messages: []schema.Message{
							schema.NewUserMessage(schema.ProtocolOpenAIChat, workflow.Get(snap, queryField)),
						},
					}, nil
				},
				func(_ workflow.Snapshot[Ticket], resp *schema.RunResponse) (workflow.Patch[Ticket], error) {
					return workflow.NewPatch(workflow.Set(categoryField, resp.Messages[0].Text())), nil
				},
			),
		},
		{
			ID:   "reply",
			Deps: []string{"classify"},
			Run: workflow.AdaptRunner(
				replier,
				func(snap workflow.Snapshot[Ticket]) (*schema.RunRequest, error) {
					if workflow.Get(snap, categoryField) == "" {
						t.Fatal("reply node did not see committed category")
					}
					return &schema.RunRequest{
						Messages: []schema.Message{
							schema.NewUserMessage(schema.ProtocolOpenAIChat, workflow.Get(snap, categoryField)),
						},
					}, nil
				},
				func(_ workflow.Snapshot[Ticket], resp *schema.RunResponse) (workflow.Patch[Ticket], error) {
					return workflow.NewPatch(workflow.Set(replyField, resp.Messages[0].Text())), nil
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := wf.Run(context.Background(), Ticket{Query: "I need a refund"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Category != "billing" || got.Reply != "We refunded your charge." {
		t.Fatalf("got %+v", got)
	}
	if classifierMeta != nil || replierMeta != nil {
		t.Fatalf("Metadata was used to pass business state: classifier=%v replier=%v", classifierMeta, replierMeta)
	}
}
