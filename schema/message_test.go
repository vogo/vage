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

package schema

import (
	"testing"

	"github.com/vogo/aimodel"
)

func TestNewUserMessage(t *testing.T) {
	msg := NewUserMessage("hello")
	if msg.Role != aimodel.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, aimodel.RoleUser)
	}
	if msg.Content.Text() != "hello" {
		t.Errorf("Content.Text() = %q, want %q", msg.Content.Text(), "hello")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
	if msg.AgentID != "" {
		t.Errorf("AgentID = %q, want empty", msg.AgentID)
	}
}

func TestNewAssistantMessage(t *testing.T) {
	aiMsg := aimodel.Message{
		Role:    aimodel.RoleAssistant,
		Content: aimodel.NewTextContent("response"),
	}
	msg := NewAssistantMessage(aiMsg, "agent-1")
	if msg.Role != aimodel.RoleAssistant {
		t.Errorf("Role = %q, want %q", msg.Role, aimodel.RoleAssistant)
	}
	if msg.Content.Text() != "response" {
		t.Errorf("Content.Text() = %q, want %q", msg.Content.Text(), "response")
	}
	if msg.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", msg.AgentID, "agent-1")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestToAIModelMessages(t *testing.T) {
	msgs := []Message{
		NewUserMessage("first"),
		NewUserMessage("second"),
	}
	aiMsgs := ToAIModelMessages(msgs)
	if len(aiMsgs) != 2 {
		t.Fatalf("len = %d, want 2", len(aiMsgs))
	}
	if aiMsgs[0].Content.Text() != "first" {
		t.Errorf("[0].Content.Text() = %q, want %q", aiMsgs[0].Content.Text(), "first")
	}
	if aiMsgs[1].Content.Text() != "second" {
		t.Errorf("[1].Content.Text() = %q, want %q", aiMsgs[1].Content.Text(), "second")
	}
}

func TestToAIModelMessages_Empty(t *testing.T) {
	aiMsgs := ToAIModelMessages(nil)
	if len(aiMsgs) != 0 {
		t.Fatalf("len = %d, want 0", len(aiMsgs))
	}
}

func TestFromAIModelMessage(t *testing.T) {
	aiMsg := aimodel.Message{
		Role:    aimodel.RoleUser,
		Content: aimodel.NewTextContent("test"),
	}
	msg := FromAIModelMessage(aiMsg)
	if msg.Role != aimodel.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, aimodel.RoleUser)
	}
	if msg.Content.Text() != "test" {
		t.Errorf("Content.Text() = %q, want %q", msg.Content.Text(), "test")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestTextResult(t *testing.T) {
	r := TextResult("call-1", "sunny weather")
	if r.ToolCallID != "call-1" {
		t.Errorf("ToolCallID = %q, want %q", r.ToolCallID, "call-1")
	}
	if r.IsError {
		t.Error("IsError should be false")
	}
	if len(r.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(r.Content))
	}
	if r.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", r.Content[0].Type, "text")
	}
	if r.Content[0].Text != "sunny weather" {
		t.Errorf("Content[0].Text = %q, want %q", r.Content[0].Text, "sunny weather")
	}
}

func TestErrorResult(t *testing.T) {
	r := ErrorResult("call-2", "something failed")
	if r.ToolCallID != "call-2" {
		t.Errorf("ToolCallID = %q, want %q", r.ToolCallID, "call-2")
	}
	if !r.IsError {
		t.Error("IsError should be true")
	}
	if len(r.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(r.Content))
	}
	if r.Content[0].Text != "something failed" {
		t.Errorf("Content[0].Text = %q, want %q", r.Content[0].Text, "something failed")
	}
}
