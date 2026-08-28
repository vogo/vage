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

package anthropics

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/schema"
)

func TestToolResultPreservesIsError(t *testing.T) {
	msg := schema.NewToolResultMessage(
		schema.ProtocolAnthropicMessages, "toolu-1", "failed", true,
	)
	wire, err := EncodeAnthropicMessage(msg)
	if err != nil {
		t.Fatalf("EncodeAnthropicMessage: %v", err)
	}

	var blocks []anthropicBlock
	if err := json.Unmarshal(wire.Content, &blocks); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(blocks) != 1 || !blocks[0].IsError {
		t.Fatalf("tool result blocks = %+v, want is_error=true", blocks)
	}

	decodedPayload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	decoded, err := DecodeAnthropicMessage(decodedPayload, "")
	if err != nil {
		t.Fatalf("DecodeAnthropicMessage: %v", err)
	}
	parts := decoded.Parts()
	if decoded.Role() != schema.RoleTool || len(parts) != 1 || !parts[0].IsError {
		t.Fatalf("decoded canonical message = role %q parts %+v", decoded.Role(), parts)
	}
}

// TestEncodeMixedContentBlocks asserts text/image/document block order,
// source type (url vs. base64), media_type and raw data for a user message
// mixing every supported media source.
func TestEncodeMixedContentBlocks(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	fileBytes := []byte("%PDF-1.4 ...")

	msg := schema.NewUserMessageWithParts(schema.ProtocolAnthropicMessages, []schema.MessagePart{
		{Type: schema.MessagePartText, Text: "see below"},
		{Type: schema.MessagePartImage, URL: "https://example.com/cat.png"},
		{Type: schema.MessagePartImage, Data: imgBytes, MimeType: "image/png"},
		{Type: schema.MessagePartFile, URL: "https://example.com/doc.pdf"},
		{Type: schema.MessagePartFile, Data: fileBytes, MimeType: "application/pdf", Filename: "report.pdf"},
	})

	wire, err := EncodeAnthropicMessage(msg)
	if err != nil {
		t.Fatalf("EncodeAnthropicMessage: %v", err)
	}

	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(wire.Content, &blocks); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(blocks) != 5 {
		t.Fatalf("len(blocks) = %d, want 5: %+v", len(blocks), blocks)
	}

	if blocks[0].Type != anthropic.ContentBlockTypeText || blocks[0].Text != "see below" {
		t.Errorf("blocks[0] = %+v, want text %q", blocks[0], "see below")
	}

	if blocks[1].Type != anthropic.ContentBlockTypeImage || blocks[1].Source == nil ||
		blocks[1].Source.Type != anthropic.ContentSourceTypeURL || blocks[1].Source.URL != "https://example.com/cat.png" {
		t.Errorf("blocks[1] = %+v, want image url source", blocks[1])
	}

	wantImgB64 := base64.StdEncoding.EncodeToString(imgBytes)
	if blocks[2].Type != anthropic.ContentBlockTypeImage || blocks[2].Source == nil ||
		blocks[2].Source.Type != anthropic.ContentSourceTypeBase64 ||
		blocks[2].Source.MediaType != "image/png" || blocks[2].Source.Data != wantImgB64 {
		t.Errorf("blocks[2] = %+v, want base64 image source", blocks[2])
	}

	if blocks[3].Type != anthropic.ContentBlockTypeDocument || blocks[3].Source == nil ||
		blocks[3].Source.Type != anthropic.ContentSourceTypeURL || blocks[3].Source.URL != "https://example.com/doc.pdf" {
		t.Errorf("blocks[3] = %+v, want document url source", blocks[3])
	}

	wantFileB64 := base64.StdEncoding.EncodeToString(fileBytes)
	if blocks[4].Type != anthropic.ContentBlockTypeDocument || blocks[4].Source == nil ||
		blocks[4].Source.Type != anthropic.ContentSourceTypeBase64 ||
		blocks[4].Source.MediaType != "application/pdf" || blocks[4].Source.Data != wantFileB64 {
		t.Errorf("blocks[4] = %+v, want base64 document source", blocks[4])
	}

	// Filename has no wire field on a document block — the documented
	// degradation. Confirm it truly does not appear anywhere in the wire JSON.
	if strings.Contains(string(wire.Content), "report.pdf") {
		t.Errorf("filename leaked into wire content: %s", wire.Content)
	}
}

// TestEncodeRejectsFileID pins the documented gap: Anthropic Messages has no
// FileID wire shape, so encoding must fail before any backend call.
func TestEncodeRejectsFileID(t *testing.T) {
	msg := schema.NewUserMessageWithParts(schema.ProtocolAnthropicMessages, []schema.MessagePart{
		{Type: schema.MessagePartFile, FileID: "file-abc123"},
	})
	if _, err := EncodeAnthropicMessage(msg); err == nil {
		t.Fatal("EncodeAnthropicMessage accepted a FileID")
	}
}

// TestEncodeRejectsMediaOnNonUserRole proves the failure happens before any
// backend call.
func TestEncodeRejectsMediaOnNonUserRole(t *testing.T) {
	msg := schema.NewMessage(schema.ProtocolAnthropicMessages, schema.RoleAssistant, []schema.MessagePart{
		{Type: schema.MessagePartImage, URL: "https://example.com/cat.png"},
	})
	if _, err := EncodeAnthropicMessage(msg); err == nil {
		t.Fatal("EncodeAnthropicMessage accepted an image part on the assistant role")
	}
}
