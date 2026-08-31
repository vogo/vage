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

import "testing"

// knownPartKinds is the canonical part vocabulary this package supports. It is
// spelled out independently of messagePartRules so the two must be changed
// together when a kind is added.
var knownPartKinds = []MessagePartType{
	MessagePartText,
	MessagePartThinking,
	MessagePartToolCall,
	MessagePartToolResult,
	MessagePartImage,
	MessagePartFile,
}

// partBaselines is a minimal valid part per kind, with a role it is allowed
// on. Field-injection and role tests perturb exactly one aspect of these.
var partBaselines = map[MessagePartType]struct {
	role Role
	part MessagePart
}{
	MessagePartText:     {RoleUser, MessagePart{Type: MessagePartText, Text: "hi"}},
	MessagePartThinking: {RoleAssistant, MessagePart{Type: MessagePartThinking, Thinking: "hmm"}},
	MessagePartToolCall: {RoleAssistant, MessagePart{
		Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search"},
	}},
	MessagePartToolResult: {RoleTool, MessagePart{
		Type: MessagePartToolResult, ToolCallID: "call-1", Text: "ok",
	}},
	MessagePartImage: {RoleUser, MessagePart{Type: MessagePartImage, URL: "https://x/y.png"}},
	MessagePartFile:  {RoleUser, MessagePart{Type: MessagePartFile, FileID: "file-1"}},
}

// fieldSetters writes a non-zero value into each optional field, so a test can
// inject a field a kind does not allow without hand-writing every combination.
var fieldSetters = map[messagePartField]func(*MessagePart){
	fieldText:       func(p *MessagePart) { p.Text = "injected" },
	fieldThinking:   func(p *MessagePart) { p.Thinking = "injected" },
	fieldToolCall:   func(p *MessagePart) { p.ToolCall = &ToolCall{ID: "call-9", Name: "injected"} },
	fieldToolCallID: func(p *MessagePart) { p.ToolCallID = "call-9" },
	fieldIsError:    func(p *MessagePart) { p.IsError = true },
	fieldURL:        func(p *MessagePart) { p.URL = "https://x/injected.png" },
	fieldData:       func(p *MessagePart) { p.Data = []byte{1} },
	fieldMimeType:   func(p *MessagePart) { p.MimeType = "image/png" },
	fieldFileID:     func(p *MessagePart) { p.FileID = "file-9" },
	fieldFilename:   func(p *MessagePart) { p.Filename = "injected.pdf" },
}

// validateParts runs Validate on a message carrying parts under role.
func validateParts(role Role, parts ...MessagePart) error {
	return NewMessage(ProtocolOpenAIChat, role, parts).Validate()
}

// TestMessagePartRuleTableCoverage pins the table as the vocabulary: every
// known kind has exactly one row, the table holds nothing else, and an
// unlisted kind fails validation instead of passing unchecked.
func TestMessagePartRuleTableCoverage(t *testing.T) {
	if len(messagePartRules) != len(knownPartKinds) {
		t.Fatalf("messagePartRules has %d rows, want %d", len(messagePartRules), len(knownPartKinds))
	}
	for _, kind := range knownPartKinds {
		if _, ok := messagePartRules[kind]; !ok {
			t.Errorf("messagePartRules is missing a row for %q", kind)
		}
	}

	if err := validateParts(RoleUser, MessagePart{Type: "audio"}); err == nil {
		t.Error("Validate(unknown part type) = nil, want an error")
	}
	if err := validateParts(RoleUser, MessagePart{Type: ""}); err == nil {
		t.Error("Validate(empty part type) = nil, want an error")
	}
}

// TestMessagePartRuleTableConsistency checks each row is internally coherent,
// so a malformed row fails here rather than as a silently unreachable rule.
func TestMessagePartRuleTableConsistency(t *testing.T) {
	for kind, rule := range messagePartRules {
		if rule.roles == 0 {
			t.Errorf("%s: allows no role, every part of this kind would fail", kind)
		}
		if extra := rule.required &^ rule.fields; extra != 0 {
			t.Errorf("%s: requires %s, which it does not allow", kind, fieldNames(extra))
		}

		var seen messagePartField
		for _, source := range rule.sources {
			if source.field&(source.field-1) != 0 {
				t.Errorf("%s: source discriminator %s is not a single field", kind, fieldNames(source.field))
			}
			if seen&source.field != 0 {
				t.Errorf("%s: duplicate source discriminator %s", kind, fieldNames(source.field))
			}
			seen |= source.field
			if extra := (source.field | source.aux) &^ rule.fields; extra != 0 {
				t.Errorf("%s: source %s references %s, which the kind does not allow",
					kind, fieldNames(source.field), fieldNames(extra))
			}
			if extra := source.required &^ source.aux; extra != 0 {
				t.Errorf("%s: source %s requires %s outside its auxiliary fields",
					kind, fieldNames(source.field), fieldNames(extra))
			}
		}
	}

	for _, spec := range messagePartFieldSpecs {
		if _, ok := fieldSetters[spec.bit]; !ok {
			t.Errorf("fieldSetters is missing %s; field injection would skip it", spec.name)
		}
	}
	if len(fieldSetters) != len(messagePartFieldSpecs) {
		t.Errorf("fieldSetters has %d entries, want %d", len(fieldSetters), len(messagePartFieldSpecs))
	}
}

// TestMessagePartRuleFieldInjection is the regression net the per-kind
// branches kept tearing: for every kind, every field it does not declare must
// be rejected when hung off an otherwise valid part.
func TestMessagePartRuleFieldInjection(t *testing.T) {
	for _, kind := range knownPartKinds {
		baseline := partBaselines[kind]
		rule := messagePartRules[kind]

		t.Run(string(kind)+" baseline", func(t *testing.T) {
			if err := validateParts(baseline.role, baseline.part); err != nil {
				t.Fatalf("Validate(baseline) = %v, want nil", err)
			}
		})

		for _, spec := range messagePartFieldSpecs {
			if rule.fields&spec.bit != 0 {
				continue
			}
			t.Run(string(kind)+" carries "+spec.name, func(t *testing.T) {
				part := cloneMessagePart(baseline.part)
				fieldSetters[spec.bit](&part)
				if err := validateParts(baseline.role, part); err == nil {
					t.Fatalf("Validate(%s with %s) = nil, want an error", kind, spec.name)
				}
			})
		}
	}
}

// TestMessagePartRuleRoleMatrix asserts the role column: image and file are
// user-only, and the refactor did not invent part-level role limits for the
// other kinds. A tool message needs a tool_result to satisfy the message-level
// rule, so non-result baselines are paired with one there.
func TestMessagePartRuleRoleMatrix(t *testing.T) {
	toolResult := partBaselines[MessagePartToolResult].part

	for _, kind := range knownPartKinds {
		baseline := partBaselines[kind]
		rule := messagePartRules[kind]

		for role := range roleBits {
			wantErr := rule.roles&roleBits[role] == 0

			t.Run(string(kind)+" on "+string(role), func(t *testing.T) {
				parts := []MessagePart{baseline.part}
				if role == RoleTool && kind != MessagePartToolResult {
					parts = append(parts, toolResult)
				}
				err := validateParts(role, parts...)
				if wantErr && err == nil {
					t.Fatalf("Validate(%s on %s) = nil, want an error", kind, role)
				}
				if !wantErr && err != nil {
					t.Fatalf("Validate(%s on %s) = %v, want nil", kind, role, err)
				}
			})
		}
	}
}

// TestMessagePartRuleCombinations covers at least one legal and one illegal
// field combination per kind, including the zero values an allowed field is
// still permitted to hold.
func TestMessagePartRuleCombinations(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		parts   []MessagePart
		wantErr bool
	}{
		{"text", RoleAssistant, []MessagePart{{Type: MessagePartText, Text: "hi"}}, false},
		// An allowed field at its zero value stays legal: empty text is a
		// valid part, not a missing one.
		{"text empty is legal", RoleUser, []MessagePart{{Type: MessagePartText}}, false},
		{"text carries thinking", RoleUser, []MessagePart{
			{Type: MessagePartText, Text: "hi", Thinking: "hmm"},
		}, true},

		{"thinking", RoleAssistant, []MessagePart{{Type: MessagePartThinking, Thinking: "hmm"}}, false},
		{"thinking empty is legal", RoleAssistant, []MessagePart{{Type: MessagePartThinking}}, false},
		{"thinking carries text", RoleAssistant, []MessagePart{
			{Type: MessagePartThinking, Thinking: "hmm", Text: "hi"},
		}, true},

		{"tool_call", RoleAssistant, []MessagePart{
			{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search", Arguments: `{"q":"x"}`}},
		}, false},
		{"tool_call without arguments", RoleAssistant, []MessagePart{
			{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search"}},
		}, false},
		{"tool_call nil payload", RoleAssistant, []MessagePart{{Type: MessagePartToolCall}}, true},
		{"tool_call without name", RoleAssistant, []MessagePart{
			{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1"}},
		}, true},
		{"tool_call invalid arguments", RoleAssistant, []MessagePart{
			{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search", Arguments: "{"}},
		}, true},
		{"tool_call carries tool_call_id", RoleAssistant, []MessagePart{
			{Type: MessagePartToolCall, ToolCall: &ToolCall{ID: "call-1", Name: "search"}, ToolCallID: "call-1"},
		}, true},

		{"tool_result", RoleTool, []MessagePart{
			{Type: MessagePartToolResult, ToolCallID: "call-1", Text: "ok"},
		}, false},
		// text and is_error are optional companions of the correlation id.
		{"tool_result error without text", RoleTool, []MessagePart{
			{Type: MessagePartToolResult, ToolCallID: "call-1", IsError: true},
		}, false},
		{"tool_result without tool_call_id", RoleTool, []MessagePart{
			{Type: MessagePartToolResult, Text: "ok"},
		}, true},
		{"tool_result carries thinking", RoleTool, []MessagePart{
			{Type: MessagePartToolResult, ToolCallID: "call-1", Thinking: "hmm"},
		}, true},
		{"tool message without tool_result", RoleTool, []MessagePart{
			{Type: MessagePartText, Text: "ok"},
		}, true},

		{"image url", RoleUser, []MessagePart{{Type: MessagePartImage, URL: "https://x/y.png"}}, false},
		{"image data", RoleUser, []MessagePart{
			{Type: MessagePartImage, Data: []byte{1}, MimeType: "image/png"},
		}, false},
		{"image both sources", RoleUser, []MessagePart{
			{Type: MessagePartImage, URL: "https://x/y.png", Data: []byte{1}, MimeType: "image/png"},
		}, true},
		{"image url carries mime", RoleUser, []MessagePart{
			{Type: MessagePartImage, URL: "https://x/y.png", MimeType: "image/png"},
		}, true},
		{"image data without mime", RoleUser, []MessagePart{{Type: MessagePartImage, Data: []byte{1}}}, true},
		{"image data with non-image mime", RoleUser, []MessagePart{
			{Type: MessagePartImage, Data: []byte{1}, MimeType: "application/pdf"},
		}, true},
		// Empty inline bytes are not a source, so this reads as "no source".
		{"image empty data is no source", RoleUser, []MessagePart{
			{Type: MessagePartImage, Data: []byte{}, MimeType: "image/png"},
		}, true},

		{"file url", RoleUser, []MessagePart{{Type: MessagePartFile, URL: "https://x/r.pdf"}}, false},
		{"file id", RoleUser, []MessagePart{{Type: MessagePartFile, FileID: "file-1"}}, false},
		{"file data with filename", RoleUser, []MessagePart{
			{Type: MessagePartFile, Data: []byte{1}, MimeType: "application/pdf", Filename: "r.pdf"},
		}, false},
		// Filename is optional on the inline source, unlike MimeType.
		{"file data without filename", RoleUser, []MessagePart{
			{Type: MessagePartFile, Data: []byte{1}, MimeType: "application/pdf"},
		}, false},
		{"file no source", RoleUser, []MessagePart{{Type: MessagePartFile}}, true},
		{"file three sources", RoleUser, []MessagePart{
			{Type: MessagePartFile, URL: "https://x/r.pdf", Data: []byte{1}, MimeType: "application/pdf", FileID: "file-1"},
		}, true},
		{"file id carries filename", RoleUser, []MessagePart{
			{Type: MessagePartFile, FileID: "file-1", Filename: "r.pdf"},
		}, true},

		// Multimodal mixing stays legal, and one invalid part fails the whole
		// message however many valid siblings it has.
		{"text with image and file", RoleUser, []MessagePart{
			{Type: MessagePartText, Text: "look"},
			{Type: MessagePartImage, URL: "https://x/y.png"},
			{Type: MessagePartFile, Data: []byte{1}, MimeType: "application/pdf", Filename: "r.pdf"},
		}, false},
		{"one invalid part among valid ones", RoleUser, []MessagePart{
			{Type: MessagePartText, Text: "look"},
			{Type: MessagePartImage, URL: "https://x/y.png", FileID: "file-1"},
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParts(tt.role, tt.parts...)
			if tt.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
