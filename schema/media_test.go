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
	"bytes"
	"testing"
)

func TestImageFromURL(t *testing.T) {
	part, err := ImageFromURL("https://example.com/cat.png")
	if err != nil {
		t.Fatalf("ImageFromURL: %v", err)
	}

	if part.Type != MessagePartImage || part.URL != "https://example.com/cat.png" {
		t.Fatalf("part = %+v", part)
	}

	if err := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{part}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if _, err := ImageFromURL(""); err == nil {
		t.Fatal("empty URL must fail")
	}
}

func TestImageFromBytes(t *testing.T) {
	src := []byte{0x89, 0x50, 0x4e, 0x47}
	part, err := ImageFromBytes(src, "image/png")
	if err != nil {
		t.Fatalf("ImageFromBytes: %v", err)
	}

	src[0] = 0
	if bytes.Equal(part.Data, src) {
		t.Fatal("part must not alias the caller's slice")
	}

	if part.MimeType != "image/png" {
		t.Fatalf("mime = %q", part.MimeType)
	}

	if err := NewUserMessageWithParts(ProtocolAnthropicMessages, []MessagePart{part}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if _, err := ImageFromBytes(nil, "image/png"); err == nil {
		t.Fatal("empty bytes must fail")
	}

	if _, err := ImageFromBytes([]byte{1}, ""); err == nil {
		t.Fatal("missing MIME must fail")
	}

	if _, err := ImageFromBytes([]byte{1}, "application/pdf"); err == nil {
		t.Fatal("non-image MIME must fail")
	}
}

func TestFileFromID(t *testing.T) {
	part, err := FileFromID("file-abc")
	if err != nil {
		t.Fatalf("FileFromID: %v", err)
	}

	if part.Type != MessagePartFile || part.FileID != "file-abc" {
		t.Fatalf("part = %+v", part)
	}

	if err := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{part}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if _, err := FileFromID(""); err == nil {
		t.Fatal("empty id must fail")
	}
}

func TestFileFromBytes(t *testing.T) {
	src := []byte("%PDF")
	part, err := FileFromBytes(src, "application/pdf", "report.pdf")
	if err != nil {
		t.Fatalf("FileFromBytes: %v", err)
	}

	src[0] = 'x'
	if bytes.Equal(part.Data, src) {
		t.Fatal("part must not alias the caller's slice")
	}

	if part.Filename != "report.pdf" || part.MimeType != "application/pdf" {
		t.Fatalf("part = %+v", part)
	}

	unnamed, err := FileFromBytes([]byte{1}, "application/pdf", "")
	if err != nil {
		t.Fatalf("empty filename must be allowed at the canonical layer: %v", err)
	}

	if unnamed.Filename != "" {
		t.Fatalf("filename = %q, want empty", unnamed.Filename)
	}

	if err := NewUserMessageWithParts(ProtocolAnthropicMessages, []MessagePart{unnamed}).Validate(); err != nil {
		t.Fatalf("Validate unnamed: %v", err)
	}

	if _, err := FileFromBytes(nil, "application/pdf", "a.pdf"); err == nil {
		t.Fatal("empty bytes must fail")
	}

	if _, err := FileFromBytes([]byte{1}, "", "a.pdf"); err == nil {
		t.Fatal("missing MIME must fail")
	}
}
