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

package openais

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/schema"
)

func TestMessageOriginReplayAndInvalidation(t *testing.T) {
	payload := json.RawMessage(`{"role":"assistant","content":"original","name":"speaker","refusal":"kept"}`)
	msg, err := DecodeOpenAIMessage(payload, "agent")
	if err != nil {
		t.Fatalf("DecodeOpenAIMessage: %v", err)
	}
	if msg.Role() != schema.RoleAssistant || msg.Text() != "original" {
		t.Fatalf("canonical message = role %q text %q", msg.Role(), msg.Text())
	}

	replayed, err := EncodeOpenAIMessage(msg)
	if err != nil {
		t.Fatalf("EncodeOpenAIMessage replay: %v", err)
	}
	if replayed.Name != "speaker" || replayed.Refusal != "kept" {
		t.Fatalf("native replay lost fields: %+v", replayed)
	}

	msg.SetText("rewritten")
	if len(msg.Origin()) != 0 {
		t.Fatal("origin retained after SetText")
	}
	encoded, err := EncodeOpenAIMessage(msg)
	if err != nil {
		t.Fatalf("EncodeOpenAIMessage canonical: %v", err)
	}
	if got := encoded.Content.Text(); got != "rewritten" {
		t.Fatalf("encoded content = %q, want rewritten", got)
	}
	if encoded.Name != "" || encoded.Refusal != "" {
		t.Fatalf("stale origin fields replayed after mutation: %+v", encoded)
	}
}

func TestEncodeRejectsInvalidCanonicalPart(t *testing.T) {
	msg := schema.NewMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, []schema.MessagePart{{
		Type: schema.MessagePartToolCall,
	}})
	if _, err := EncodeOpenAIMessage(msg); err == nil {
		t.Fatal("EncodeOpenAIMessage accepted nil tool call")
	}
}

func TestEncodeValidatesCanonicalStateBeforeOriginReplay(t *testing.T) {
	msg := schema.NewMessageWithOrigin(
		schema.ProtocolOpenAIChat,
		schema.RoleAssistant,
		[]schema.MessagePart{{Type: schema.MessagePartToolCall}},
		json.RawMessage(`{"role":"assistant","content":"stale"}`),
		"",
	)
	if _, err := EncodeOpenAIMessage(msg); err == nil {
		t.Fatal("EncodeOpenAIMessage replayed origin for invalid canonical state")
	}
}

// TestEncodeTextOnlyMessageKeepsScalarContent proves media-free messages are
// unaffected by this change: they still encode to the scalar Content form,
// not a one-element parts array.
func TestEncodeTextOnlyMessageKeepsScalarContent(t *testing.T) {
	msg := schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")

	encoded, err := EncodeOpenAIMessage(msg)
	if err != nil {
		t.Fatalf("EncodeOpenAIMessage: %v", err)
	}
	if encoded.Content.Text() != "hello" {
		t.Fatalf("Content.Text() = %q, want %q", encoded.Content.Text(), "hello")
	}
	if encoded.Content.Parts() != nil {
		t.Fatalf("Content.Parts() = %+v, want nil (scalar form)", encoded.Content.Parts())
	}

	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var probe struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("wire content is not a JSON string: %s", raw)
	}
}

// TestEncodeMixedContentParts asserts the wire content array produced for a
// user message mixing text, an image URL, an inline base64 image, an inline
// file and an OpenAI FileID, in canonical order.
func TestEncodeMixedContentParts(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	fileBytes := []byte("%PDF-1.4 ...")

	msg := schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
		{Type: schema.MessagePartText, Text: "see below"},
		{Type: schema.MessagePartImage, URL: "https://example.com/cat.png"},
		{Type: schema.MessagePartImage, Data: imgBytes, MimeType: "image/png"},
		{Type: schema.MessagePartFile, Data: fileBytes, MimeType: "application/pdf", Filename: "report.pdf"},
		{Type: schema.MessagePartFile, FileID: "file-abc123"},
	})

	encoded, err := EncodeOpenAIMessage(msg)
	if err != nil {
		t.Fatalf("EncodeOpenAIMessage: %v", err)
	}

	parts := encoded.Content.Parts()
	if len(parts) != 5 {
		t.Fatalf("len(Parts()) = %d, want 5: %+v", len(parts), parts)
	}

	if parts[0].Type != openai.ContentPartTypeText || parts[0].Text != "see below" {
		t.Errorf("parts[0] = %+v, want text %q", parts[0], "see below")
	}

	if parts[1].Type != openai.ContentPartTypeImageURL || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != "https://example.com/cat.png" {
		t.Errorf("parts[1] = %+v, want image_url pointing at the URL", parts[1])
	}

	wantImgDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
	if parts[2].Type != openai.ContentPartTypeImageURL || parts[2].ImageURL == nil ||
		parts[2].ImageURL.URL != wantImgDataURI {
		t.Errorf("parts[2] = %+v, want image_url data URI %q", parts[2], wantImgDataURI)
	}

	wantFileDataURI := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(fileBytes)
	if parts[3].Type != openai.ContentPartTypeFile || parts[3].File == nil ||
		parts[3].File.FileData != wantFileDataURI || parts[3].File.Filename != "report.pdf" {
		t.Errorf("parts[3] = %+v, want file.file_data %q with filename report.pdf", parts[3], wantFileDataURI)
	}

	if parts[4].Type != openai.ContentPartTypeFile || parts[4].File == nil || parts[4].File.FileID != "file-abc123" {
		t.Errorf("parts[4] = %+v, want file.file_id %q", parts[4], "file-abc123")
	}

	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wireParts []map[string]any
	var wire struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire message: %v", err)
	}
	if err := json.Unmarshal(wire.Content, &wireParts); err != nil {
		t.Fatalf("wire content is not a JSON array: %s", wire.Content)
	}
	if len(wireParts) != 5 {
		t.Fatalf("wire content array has %d entries, want 5", len(wireParts))
	}
}

// TestEncodeRejectsFileURL pins the documented gap: OpenAI Chat Completions
// has no file-by-URL wire shape, so encoding must fail before any backend
// call rather than silently drop the source.
func TestEncodeConstructorBuiltParts(t *testing.T) {
	imgURL, err := schema.ImageFromURL("https://example.com/cat.png")
	if err != nil {
		t.Fatal(err)
	}

	imgBytes, err := schema.ImageFromBytes([]byte{0x89, 0x50, 0x4e, 0x47}, "image/png")
	if err != nil {
		t.Fatal(err)
	}

	fileBytes, err := schema.FileFromBytes([]byte("%PDF"), "application/pdf", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}

	fileID, err := schema.FileFromID("file-abc123")
	if err != nil {
		t.Fatal(err)
	}

	msg := schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
		{Type: schema.MessagePartText, Text: "see"},
		imgURL, imgBytes, fileBytes, fileID,
	})
	encoded, err := EncodeOpenAIMessage(msg)
	if err != nil {
		t.Fatalf("EncodeOpenAIMessage: %v", err)
	}

	if got := len(encoded.Content.Parts()); got != 5 {
		t.Fatalf("parts = %d, want 5", got)
	}

	unnamed, err := schema.FileFromBytes([]byte("data"), "application/pdf", "")
	if err != nil {
		t.Fatalf("canonical unnamed file must be allowed: %v", err)
	}

	if _, err := EncodeOpenAIMessage(schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{unnamed})); err == nil {
		t.Fatal("OpenAI must still reject unnamed inline files")
	}
}

func TestEncodeRejectsFileURL(t *testing.T) {
	msg := schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
		{Type: schema.MessagePartFile, URL: "https://example.com/report.pdf"},
	})
	if _, err := EncodeOpenAIMessage(msg); err == nil {
		t.Fatal("EncodeOpenAIMessage accepted a file URL")
	}
}

// TestEncodeRejectsInlineFileWithoutFilename pins the other documented gap:
// OpenAI requires filename alongside inline file_data.
func TestEncodeRejectsInlineFileWithoutFilename(t *testing.T) {
	msg := schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
		{Type: schema.MessagePartFile, Data: []byte("data"), MimeType: "application/pdf"},
	})
	if _, err := EncodeOpenAIMessage(msg); err == nil {
		t.Fatal("EncodeOpenAIMessage accepted inline file data without a filename")
	}
}

// TestEncodeRejectsMediaOnNonUserRole proves the failure happens before any
// backend call, for both roles that cannot legally carry media.
func TestEncodeRejectsMediaOnNonUserRole(t *testing.T) {
	for _, role := range []schema.Role{schema.RoleSystem, schema.RoleAssistant} {
		t.Run(string(role), func(t *testing.T) {
			msg := schema.NewMessage(schema.ProtocolOpenAIChat, role, []schema.MessagePart{
				{Type: schema.MessagePartImage, URL: "https://example.com/cat.png"},
			})
			if _, err := EncodeOpenAIMessage(msg); err == nil {
				t.Fatalf("EncodeOpenAIMessage accepted an image part on role %q", role)
			}
		})
	}
}
