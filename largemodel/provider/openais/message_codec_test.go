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
	"encoding/json"
	"testing"

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
