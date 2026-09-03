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
	"fmt"
	"strings"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/workflow"
)

// Ticket is the typed state SupportFlow threads between nodes. Business
// fields live here, not in RunResponse.Metadata.
type Ticket struct {
	Query    string
	Category string
	Reply    string
}

var (
	queryField = workflow.NewField(
		"query",
		func(s Ticket) string { return s.Query },
		func(s *Ticket, v string) { s.Query = v },
	)
	categoryField = workflow.NewField(
		"category",
		func(s Ticket) string { return s.Category },
		func(s *Ticket, v string) { s.Category = v },
	)
	replyField = workflow.NewField(
		"reply",
		func(s Ticket) string { return s.Reply },
		func(s *Ticket, v string) { s.Reply = v },
	)
)

// ExampleSupportFlow classifies a ticket then drafts a reply. Each Agent
// node maps Snapshot ↔ RunRequest/RunResponse explicitly; Metadata stays
// empty. This is the typed `workflow` package, not agent/workflowagent.
func Example_supportFlow() {
	classifier := agent.NewCustomAgent(
		agent.Config{ID: "classifier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			q := ""
			if len(req.Messages) > 0 {
				q = req.Messages[0].Text()
			}
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
		agent.Config{ID: "replier"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			cat := ""
			if len(req.Messages) > 0 {
				cat = req.Messages[0].Text()
			}
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

	classify := workflow.AdaptRunner(
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
	)

	reply := workflow.AdaptRunner(
		replier,
		func(snap workflow.Snapshot[Ticket]) (*schema.RunRequest, error) {
			return &schema.RunRequest{
				Messages: []schema.Message{
					schema.NewUserMessage(schema.ProtocolOpenAIChat, workflow.Get(snap, categoryField)),
				},
			}, nil
		},
		func(_ workflow.Snapshot[Ticket], resp *schema.RunResponse) (workflow.Patch[Ticket], error) {
			return workflow.NewPatch(workflow.Set(replyField, resp.Messages[0].Text())), nil
		},
	)

	wf, err := workflow.New([]workflow.Node[Ticket]{
		{ID: "classify", Run: classify},
		{ID: "reply", Deps: []string{"classify"}, Run: reply},
	})
	if err != nil {
		panic(err)
	}

	got, err := wf.Run(context.Background(), Ticket{Query: "Please refund my payment"})
	if err != nil {
		panic(err)
	}

	fmt.Printf("category=%s\nreply=%s\n", got.Category, got.Reply)
	// Output:
	// category=billing
	// reply=We refunded your charge.
}
