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
	"strings"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

func TestAgent_SharedManager_DoesNotLeakAcrossSessions(t *testing.T) {
	shared := memory.NewMapStore()
	session := memory.NewSessionMemoryWithStore(shared, "iso-agent", "unused")
	mgr := memory.NewManager(memory.WithSession(session))

	run := func(t *testing.T, sessionID, userText, reply string) *mockCaller {
		t.Helper()
		mock := newMock(stopResponse(reply))
		a := New(
			agent.Config{ID: "iso-agent"},
			WithCaller(mock),
			WithMemory(mgr),
		)
		if _, err := a.Run(context.Background(), &schema.RunRequest{
			SessionID: sessionID,
			Messages:  []schema.Message{schema.NewUserMessage(testProtocol, userText)},
		}); err != nil {
			t.Fatalf("Run(%s): %v", sessionID, err)
		}
		return mock
	}

	run(t, "sess-A", "secret-from-A", "answer-A")
	mockB := run(t, "sess-B", "question-B", "answer-B")

	if len(mockB.Requests()) == 0 {
		t.Fatal("Run-B made no model request")
	}
	for _, msg := range mockB.Requests()[0].Messages {
		if strings.Contains(msg.Text(), "secret-from-A") {
			t.Fatalf("Run-B prompt leaked Run-A history: %q", msg.Text())
		}
	}

	mockA2 := run(t, "sess-A", "follow-up-A", "answer-A2")
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

func TestAgent_RunStream_WritesScopedSession(t *testing.T) {
	shared := memory.NewMapStore()
	session := memory.NewSessionMemoryWithStore(shared, "iso-agent", "unused")
	mgr := memory.NewManager(memory.WithSession(session))

	streamMock := streamingMock()
	a := New(
		agent.Config{ID: "iso-agent"},
		WithCaller(streamMock),
		WithMemory(mgr),
	)
	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		SessionID: "sess-C",
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "stream-hi")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if _, err := drainStreamEvents(t, rs); err != nil {
		t.Fatalf("drain: %v", err)
	}

	mockFollow := newMock(stopResponse("follow"))
	follow := New(
		agent.Config{ID: "iso-agent"},
		WithCaller(mockFollow),
		WithMemory(mgr),
	)
	if _, err := follow.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-C",
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "next")},
	}); err != nil {
		t.Fatalf("follow-up Run: %v", err)
	}

	foundUser, foundOther := false, false
	for _, msg := range mockFollow.Requests()[0].Messages {
		if msg.Text() == "stream-hi" {
			foundUser = true
		}
		if strings.Contains(msg.Text(), "secret-from-A") {
			foundOther = true
		}
	}
	if !foundUser {
		t.Fatal("stream write did not land in sess-C history")
	}
	if foundOther {
		t.Fatal("sess-C follow-up leaked another session")
	}
}
