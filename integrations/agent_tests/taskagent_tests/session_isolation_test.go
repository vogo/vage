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

package taskagent_tests //nolint:revive // integration test package

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

func TestSharedManager_RunBPromptExcludesRunA(t *testing.T) {
	shared := memory.NewMapStore()
	session := memory.NewSessionMemoryWithStore(shared, "iso-agent", "unused")
	mgr := memory.NewManager(memory.WithSession(session))

	mockA := newMock(makeStopResponse("answer-A", 20))
	a := taskagent.New(
		agent.Config{ID: "iso-agent"},
		taskagent.WithCaller(mockA),
		taskagent.WithMemory(mgr),
	)
	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "run-A",
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "secret-from-A")},
	}); err != nil {
		t.Fatalf("Run-A: %v", err)
	}

	mockB := newMock(makeStopResponse("answer-B", 20))
	b := taskagent.New(
		agent.Config{ID: "iso-agent"},
		taskagent.WithCaller(mockB),
		taskagent.WithMemory(mgr),
	)
	if _, err := b.Run(context.Background(), &schema.RunRequest{
		SessionID: "run-B",
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "question-B")},
	}); err != nil {
		t.Fatalf("Run-B: %v", err)
	}

	reqs := mockB.Requests()
	if len(reqs) == 0 {
		t.Fatal("Run-B made no model request")
	}
	for _, msg := range reqs[0].Messages {
		if strings.Contains(msg.Text(), "secret-from-A") {
			t.Fatalf("Run-B prompt contained Run-A history: %q", msg.Text())
		}
	}

	mockA2 := newMock(makeStopResponse("answer-A2", 20))
	a2 := taskagent.New(
		agent.Config{ID: "iso-agent"},
		taskagent.WithCaller(mockA2),
		taskagent.WithMemory(mgr),
	)
	if _, err := a2.Run(context.Background(), &schema.RunRequest{
		SessionID: "run-A",
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "follow-up-A")},
	}); err != nil {
		t.Fatalf("Run-A follow-up: %v", err)
	}
	found := false
	for _, msg := range mockA2.Requests()[0].Messages {
		if msg.Text() == "secret-from-A" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("same-session follow-up is missing its own history")
	}
}
