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
	"reflect"
	"testing"
)

// bothProtocols is the table every wire-form-agnostic test runs against, so
// the accessors are proven to behave identically on each stored wire form.
var bothProtocols = []Protocol{ProtocolOpenAIChat, ProtocolAnthropicMessages}

func TestNewUserMessage(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			msg := NewUserMessage(proto, "hello")

			if msg.Protocol() != proto {
				t.Errorf("Protocol = %q, want %q", msg.Protocol(), proto)
			}
			if msg.Role() != RoleUser {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleUser)
			}
			if msg.Text() != "hello" {
				t.Errorf("Text() = %q, want %q", msg.Text(), "hello")
			}
			if len(msg.Parts()) != 1 || msg.Parts()[0].Type != MessagePartText {
				t.Errorf("Parts = %+v, want one text part", msg.Parts())
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

	if len(NewSystemMessage(ProtocolOpenAIChat, "be brief").Origin()) != 0 {
		t.Error("newly constructed system messages should rely on canonical fields")
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

			if decoded.Protocol() != proto {
				t.Errorf("Protocol = %q, want %q", decoded.Protocol(), proto)
			}
			if decoded.Role() != original.Role() {
				t.Errorf("Role = %q, want %q", decoded.Role(), original.Role())
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

func TestCanonicalMutationInvalidatesOrigin(t *testing.T) {
	payload := json.RawMessage(`{"role":"assistant","content":"hi"}`)
	msg := NewMessageWithOrigin(
		ProtocolOpenAIChat,
		RoleAssistant,
		[]MessagePart{{Type: MessagePartText, Text: "hi"}},
		payload,
		"",
	)
	if len(msg.Origin()) == 0 {
		t.Fatal("origin empty before mutation")
	}

	msg.SetText("rewritten")
	if len(msg.Origin()) != 0 {
		t.Fatal("origin retained after canonical mutation")
	}
	if msg.Text() != "rewritten" {
		t.Fatalf("Text() = %q, want rewritten", msg.Text())
	}
}

func TestPartsReturnsDeepCopy(t *testing.T) {
	msg := NewAssistantTurn(ProtocolOpenAIChat, "", "", []ToolCall{{
		ID: "call-1", Name: "run", Arguments: `{}`,
	}})
	parts := msg.Parts()
	parts[0].ToolCall.Name = "changed"
	if got := msg.ToolCalls()[0].Name; got != "run" {
		t.Fatalf("message mutated through Parts copy: %q", got)
	}
}

func TestSetTextDoesNotMutateMessageCopy(t *testing.T) {
	original := NewAssistantTurn(ProtocolOpenAIChat, "original", "", nil)
	mutated := original
	mutated.SetText("changed")
	if got := original.Text(); got != "original" {
		t.Fatalf("original Text() = %q after copy mutation", got)
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

// TestNewUserMessageWithParts proves text/image/file can be mixed in a single
// user message, in the caller's order, and that Text() reports only the text
// parts — media never leaks into it.
func TestNewUserMessageWithParts(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			parts := []MessagePart{
				{Type: MessagePartText, Text: "look at this"},
				{Type: MessagePartImage, URL: "https://example.com/cat.png"},
				{Type: MessagePartFile, Data: []byte("pdf-bytes"), MimeType: "application/pdf", Filename: "report.pdf"},
			}
			msg := NewUserMessageWithParts(proto, parts)

			if err := msg.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if msg.Protocol() != proto {
				t.Errorf("Protocol() = %q, want %q", msg.Protocol(), proto)
			}
			if msg.Role() != RoleUser {
				t.Errorf("Role() = %q, want %q", msg.Role(), RoleUser)
			}
			if msg.Text() != "look at this" {
				t.Errorf("Text() = %q, want only the text part", msg.Text())
			}

			got := msg.Parts()
			if len(got) != 3 {
				t.Fatalf("len(Parts()) = %d, want 3", len(got))
			}
			if got[0].Type != MessagePartText || got[1].Type != MessagePartImage || got[2].Type != MessagePartFile {
				t.Errorf("Parts() order = %+v, want text, image, file", got)
			}
			if got[1].URL != "https://example.com/cat.png" {
				t.Errorf("image URL = %q, want preserved", got[1].URL)
			}
			if string(got[2].Data) != "pdf-bytes" || got[2].Filename != "report.pdf" {
				t.Errorf("file part = %+v, want data and filename preserved", got[2])
			}
		})
	}
}

// TestMessagePartDataIsDeepCopied guards the value-semantics contract: no
// entry point (construction, Parts(), AppendPart) may let a caller mutate a
// Message through a shared []byte.
func TestMessagePartDataIsDeepCopied(t *testing.T) {
	t.Run("construction copies the input slice", func(t *testing.T) {
		data := []byte{1, 2, 3}
		msg := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{
			{Type: MessagePartImage, Data: data, MimeType: "image/png"},
		})
		data[0] = 0xFF
		if msg.Parts()[0].Data[0] == 0xFF {
			t.Fatal("mutating the input slice changed the message")
		}
	})

	t.Run("Parts returns a copy", func(t *testing.T) {
		msg := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{
			{Type: MessagePartImage, Data: []byte{1, 2, 3}, MimeType: "image/png"},
		})
		got := msg.Parts()
		got[0].Data[0] = 0xFF
		if msg.Parts()[0].Data[0] == 0xFF {
			t.Fatal("mutating the returned slice changed the message")
		}
	})

	t.Run("AppendPart copies the appended part", func(t *testing.T) {
		msg := NewUserMessage(ProtocolOpenAIChat, "hi")
		data := []byte{1, 2, 3}
		msg.AppendPart(MessagePart{Type: MessagePartImage, Data: data, MimeType: "image/png"})
		data[0] = 0xFF
		if msg.Parts()[1].Data[0] == 0xFF {
			t.Fatal("mutating the appended slice changed the message")
		}
	})
}

// TestSetTextPreservesMedia guards the same rewrite path as
// TestSetTextPreservesToolCalls, for image/file parts instead of tool calls.
func TestSetTextPreservesMedia(t *testing.T) {
	msg := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{
		{Type: MessagePartImage, URL: "https://example.com/cat.png"},
	})
	msg.SetText("caption")

	if msg.Text() != "caption" {
		t.Errorf("Text() = %q, want %q", msg.Text(), "caption")
	}
	parts := msg.Parts()
	if len(parts) != 2 {
		t.Fatalf("len(Parts()) = %d, want 2 (image kept, text appended)", len(parts))
	}
	hasImage := false
	for _, p := range parts {
		if p.Type == MessagePartImage && p.URL == "https://example.com/cat.png" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Errorf("Parts() = %+v, want the original image preserved", parts)
	}
}

// TestMessageRoundTripWithMedia proves JSON persistence carries image/file
// parts losslessly, including raw Data bytes.
func TestMessageRoundTripWithMedia(t *testing.T) {
	for _, proto := range bothProtocols {
		t.Run(string(proto), func(t *testing.T) {
			original := NewUserMessageWithParts(proto, []MessagePart{
				{Type: MessagePartText, Text: "see attached"},
				{Type: MessagePartImage, Data: []byte{0x89, 0x50, 0x4e, 0x47}, MimeType: "image/png"},
				{Type: MessagePartFile, FileID: "file-abc"},
			})

			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var decoded Message
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			got := decoded.Parts()
			want := original.Parts()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round-tripped Parts() = %+v, want %+v", got, want)
			}
			if err := decoded.Validate(); err != nil {
				t.Errorf("decoded.Validate() = %v, want nil", err)
			}
		})
	}
}

// TestMessageValidateMedia table-drives every canonical media invariant:
// missing/multiple sources, missing MIME on inline data, a non-image MIME on
// an image part, the wrong role, and cross-type fields.
func TestMessageValidateMedia(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		part    MessagePart
		wantErr bool
	}{
		{"image url only", RoleUser, MessagePart{Type: MessagePartImage, URL: "https://x/y.png"}, false},
		{"image data with mime", RoleUser, MessagePart{Type: MessagePartImage, Data: []byte{1}, MimeType: "image/png"}, false},
		{"image missing source", RoleUser, MessagePart{Type: MessagePartImage}, true},
		{"image url and data both set", RoleUser, MessagePart{Type: MessagePartImage, URL: "https://x/y.png", Data: []byte{1}, MimeType: "image/png"}, true},
		{"image data missing mime", RoleUser, MessagePart{Type: MessagePartImage, Data: []byte{1}}, true},
		{"image non-image mime", RoleUser, MessagePart{Type: MessagePartImage, Data: []byte{1}, MimeType: "application/pdf"}, true},
		{"image on assistant role", RoleAssistant, MessagePart{Type: MessagePartImage, URL: "https://x/y.png"}, true},
		{"image on system role", RoleSystem, MessagePart{Type: MessagePartImage, URL: "https://x/y.png"}, true},
		{"image on tool role", RoleTool, MessagePart{Type: MessagePartImage, URL: "https://x/y.png"}, true},
		{"image carries tool_call_id", RoleUser, MessagePart{Type: MessagePartImage, URL: "https://x/y.png", ToolCallID: "call-1"}, true},

		{"file url only", RoleUser, MessagePart{Type: MessagePartFile, URL: "https://x/report.pdf"}, false},
		{"file data with mime", RoleUser, MessagePart{Type: MessagePartFile, Data: []byte{1}, MimeType: "application/pdf", Filename: "r.pdf"}, false},
		{"file id only", RoleUser, MessagePart{Type: MessagePartFile, FileID: "file-1"}, false},
		{"file missing source", RoleUser, MessagePart{Type: MessagePartFile}, true},
		{"file two sources", RoleUser, MessagePart{Type: MessagePartFile, URL: "https://x/report.pdf", FileID: "file-1"}, true},
		{"file data missing mime", RoleUser, MessagePart{Type: MessagePartFile, Data: []byte{1}, Filename: "r.pdf"}, true},
		{"file on assistant role", RoleAssistant, MessagePart{Type: MessagePartFile, FileID: "file-1"}, true},
		{"file carries thinking", RoleUser, MessagePart{Type: MessagePartFile, FileID: "file-1", Thinking: "x"}, true},

		// A non-media part holding a media field would be dropped by every
		// codec, which reads sources from image/file parts only. Validate must
		// reject it rather than let the caller believe the source was sent.
		{"text carries url", RoleUser, MessagePart{Type: MessagePartText, Text: "hi", URL: "https://x/y.png"}, true},
		{"text carries inline data", RoleUser, MessagePart{Type: MessagePartText, Text: "hi", Data: []byte{1}, MimeType: "image/png"}, true},
		{"text carries file id", RoleUser, MessagePart{Type: MessagePartText, Text: "hi", FileID: "file-1"}, true},
		{"text carries filename", RoleUser, MessagePart{Type: MessagePartText, Text: "hi", Filename: "r.pdf"}, true},
		{"thinking carries url", RoleAssistant, MessagePart{Type: MessagePartThinking, Thinking: "x", URL: "https://x/y.png"}, true},
		{"tool_call carries filename", RoleAssistant, MessagePart{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search"}, Filename: "r.pdf"}, true},
		{"tool_result carries url", RoleTool, MessagePart{Type: MessagePartToolResult, ToolCallID: "call-1", Text: "ok", URL: "https://x/report.pdf"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := NewMessage(ProtocolOpenAIChat, tt.role, []MessagePart{tt.part})
			err := msg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
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

func TestToolResultText(t *testing.T) {
	tests := []struct {
		name   string
		result ToolResult
		want   string
	}{
		{
			name:   "single text part",
			result: TextResult("call-1", "sunny weather"),
			want:   "sunny weather",
		},
		{
			name: "skips leading non-text parts",
			result: ToolResult{Content: []ContentPart{
				{Type: "image", Data: []byte{0x89, 0x50}, MimeType: "image/png"},
				{Type: "json", Text: `{"ignored":true}`},
				{Type: "text", Text: "after the payload"},
			}},
			want: "after the payload",
		},
		{
			name: "returns first of several text parts",
			result: ToolResult{Content: []ContentPart{
				{Type: "text", Text: "first"},
				{Type: "text", Text: "second"},
			}},
			want: "first",
		},
		{
			// Stopping at an empty first part is the contract, not an
			// oversight: it keeps what the model receives verbatim.
			name: "stops at empty first text part",
			result: ToolResult{Content: []ContentPart{
				{Type: "text", Text: ""},
				{Type: "text", Text: "not reached"},
			}},
			want: "",
		},
		{
			name: "no text part",
			result: ToolResult{Content: []ContentPart{
				{Type: "image", MimeType: "image/png"},
			}},
			want: "",
		},
		{
			name:   "nil content",
			result: ToolResult{ToolCallID: "call-9"},
			want:   "",
		},
		{
			name:   "error result reads like any other",
			result: ErrorResult("call-2", "something failed"),
			want:   "something failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Text(); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolResultTextDoesNotMutateReceiver(t *testing.T) {
	r := ToolResult{ToolCallID: "call-1", Content: []ContentPart{
		{Type: "json", Text: `{"a":1}`},
		{Type: "text", Text: "hello"},
	}}
	before := append([]ContentPart(nil), r.Content...)

	_ = r.Text()

	if r.ToolCallID != "call-1" || r.IsError {
		t.Errorf("envelope changed: %+v", r)
	}
	if len(r.Content) != len(before) {
		t.Fatalf("len(Content) = %d, want %d", len(r.Content), len(before))
	}
	if !reflect.DeepEqual(r.Content, before) {
		t.Errorf("Content = %+v, want %+v", r.Content, before)
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
