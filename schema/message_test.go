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
	"encoding/json"
	"errors"
	"testing"
)

// bothProtocols is the table every wire-form-agnostic test runs against, so
// the accessors are proven to behave identically on each stored wire form.
var bothProtocols = []Protocol{ProtocolOpenAIChat, ProtocolAnthropicMessages}

func TestNewUserMessage(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewUserMessage(proto, "hello")

			if msg.Protocol != proto {
				t.Errorf("Protocol = %q, want %q", msg.Protocol, proto)
			}
			if msg.Role() != RoleUser {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleUser)
			}
			if msg.Text() != "hello" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "hello")
			}
			if msg.Timestamp.IsZero() {
				t.Error("Timestamp should not be zero")
			}
			if msg.AgentID != "" {
				t.Errorf("AgentID = %q, want empty", msg.AgentID)
			}
		})
	}
}

// TestNewSystemMessage pins the structural difference between the two
// protocols: Anthropic has no system role, so the text is held for the
// request-level system field instead of as a message.
func TestNewSystemMessage(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewSystemMessage(proto, "be brief")

			if msg.Role() != RoleSystem {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleSystem)
			}
			if msg.Text() != "be brief" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "be brief")
			}
		})
	}

	if got := NewSystemMessage(ProtocolAnthropicMessages, "be brief").SystemText; got != "be brief" {
		t.Errorf("anthropic SystemText = %q, want %q", got, "be brief")
	}

	if NewSystemMessage(ProtocolOpenAIChat, "be brief").OpenAI == nil {
		t.Error("openai system message should carry a wire payload")
	}
}

func TestNewToolResultMessage(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewToolResultMessage(proto, "call-1", "sunny", false)

			if msg.Role() != RoleTool {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleTool)
			}
			if msg.ToolCallID() != "call-1" {
				t.Errorf("ToolCallID() = %q, want %q", msg.ToolCallID(), "call-1")
			}
			if msg.Text() != "sunny" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "sunny")
			}
		})
	}
}

func TestNewAssistantTurn(t *testing.T) {
	calls := []ToolCall{
		{ID: "call-1", Name: "get_weather", Arguments: `{"city":"SF"}`},
		{ID: "call-2", Name: "get_time", Arguments: `{"tz":"UTC"}`},
	}

	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewAssistantTurn(proto, "working on it", "let me think", calls)

			if msg.Role() != RoleAssistant {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleAssistant)
			}
			if msg.Text() != "working on it" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "working on it")
			}
			if msg.Thinking() != "let me think" {
				t.Errorf("Thinking() = %q, want %q", msg.Thinking(), "let me think")
			}

			got := msg.ToolCalls()
			if len(got) != len(calls) {
				t.Fatalf("len(ToolCalls()) = %d, want %d", len(got), len(calls))
			}

			for i, want := range calls {
				if got[i].ID != want.ID || got[i].Name != want.Name || got[i].Arguments != want.Arguments {
					t.Errorf("ToolCalls()[%d] = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// TestSetTextPreservesToolCalls guards the guard-rewrite path: replacing the
// text of an assistant turn must not drop the tool calls it carries.
func TestSetTextPreservesToolCalls(t *testing.T) {
	calls := []ToolCall{{ID: "call-1", Name: "run", Arguments: `{}`}}

	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewAssistantTurn(proto, "original", "", calls)
			msg.SetText("redacted")

			if msg.Text() != "redacted" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "redacted")
			}

			if got := msg.ToolCalls(); len(got) != 1 || got[0].ID != "call-1" {
				t.Errorf("ToolCalls() = %+v, want the original single call", got)
			}
		})
	}
}

func TestSetTextOnToolResult(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewToolResultMessage(proto, "call-1", "secret", false)
			msg.SetText("[redacted]")

			if msg.Text() != "[redacted]" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "[redacted]")
			}
			if msg.ToolCallID() != "call-1" {
				t.Errorf("ToolCallID() = %q, want it preserved", msg.ToolCallID())
			}
		})
	}
}

// TestMessageRoundTrip proves persistence is lossless: a message survives
// encode/decode with its wire form and every accessor intact. This is what
// lets checkpoints and memory store native wire forms directly.
func TestMessageRoundTrip(t *testing.T) {
	calls := []ToolCall{{ID: "call-1", Name: "run", Arguments: `{"x":1}`}}

	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			original := NewAssistantTurn(proto, "hello", "thinking", calls)

			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var decoded Message
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if decoded.Protocol != proto {
				t.Errorf("Protocol = %q, want %q", decoded.Protocol, proto)
			}
			if decoded.Text() != original.Text() {
				t.Errorf("Text() = %q, want %q", decoded.Text(), original.Text())
			}
			if decoded.Thinking() != original.Thinking() {
				t.Errorf("Thinking() = %q, want %q", decoded.Thinking(), original.Thinking())
			}

			got := decoded.ToolCalls()
			if len(got) != 1 || got[0].ID != "call-1" || got[0].Arguments != `{"x":1}` {
				t.Errorf("ToolCalls() = %+v, want the original call", got)
			}
		})
	}
}

// TestRequireProtocol covers the dual-track guarantee: a message recorded
// under one protocol must not be replayed under another.
func TestRequireProtocol(t *testing.T) {
	msg := NewUserMessage(ProtocolOpenAIChat, "hi")

	if err := msg.RequireProtocol(ProtocolOpenAIChat); err != nil {
		t.Errorf("RequireProtocol(matching) = %v, want nil", err)
	}

	err := msg.RequireProtocol(ProtocolAnthropicMessages)
	if err == nil {
		t.Fatal("RequireProtocol(mismatched) = nil, want an error")
	}

	if !errors.Is(err, ErrProtocolMismatch) {
		t.Errorf("RequireProtocol(mismatched) = %v, want ErrProtocolMismatch", err)
	}
}

func TestProtocolOf(t *testing.T) {
	if got := ProtocolOf(nil); got != ProtocolOpenAIChat {
		t.Errorf("ProtocolOf(nil) = %q, want the default %q", got, ProtocolOpenAIChat)
	}

	msgs := []Message{NewUserMessage(ProtocolAnthropicMessages, "hi")}
	if got := ProtocolOf(msgs); got != ProtocolAnthropicMessages {
		t.Errorf("ProtocolOf = %q, want %q", got, ProtocolAnthropicMessages)
	}
}

func TestTextResult(t *testing.T) {
	r := TextResult("call-1", "sunny weather")
	if r.ToolCallID() != "call-1" {
		t.Errorf("ToolCallID = %q, want %q", r.ToolCallID(), "call-1")
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
	if r.ToolCallID() != "call-2" {
		t.Errorf("ToolCallID = %q, want %q", r.ToolCallID(), "call-2")
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
