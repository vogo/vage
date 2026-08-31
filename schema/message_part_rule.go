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
	"fmt"
	"strings"
)

// MessagePart is one public struct carrying every part kind, so which fields,
// roles and media sources a kind may use is not expressible in its Go type.
// That matrix lives here as a declarative rule table read by one generic
// checker, rather than as per-kind branches in Message.Validate: adding a
// field or a part kind must be an edit to a table row, never to the checking
// algorithm.

// messagePartField is one bit per optional MessagePart field. Type is absent
// on purpose — every part carries it, so it is never part of the matrix.
type messagePartField uint16

const (
	fieldText messagePartField = 1 << iota
	fieldThinking
	fieldToolCall
	fieldToolCallID
	fieldIsError
	fieldURL
	fieldData
	fieldMimeType
	fieldFileID
	fieldFilename
)

// messagePartFieldSpecs is the one place a field's bit, wire name and "is set"
// test live. Presence is decided by the Go zero value alone; consulting JSON
// tags or a provider type would make validation depend on the codec layer it
// exists to protect.
var messagePartFieldSpecs = []struct {
	bit  messagePartField
	name string
	set  func(MessagePart) bool
}{
	{fieldText, "text", func(p MessagePart) bool { return p.Text != "" }},
	{fieldThinking, "thinking", func(p MessagePart) bool { return p.Thinking != "" }},
	{fieldToolCall, "tool_call", func(p MessagePart) bool { return p.ToolCall != nil }},
	{fieldToolCallID, "tool_call_id", func(p MessagePart) bool { return p.ToolCallID != "" }},
	{fieldIsError, "is_error", func(p MessagePart) bool { return p.IsError }},
	{fieldURL, "url", func(p MessagePart) bool { return p.URL != "" }},
	{fieldData, "data", func(p MessagePart) bool { return len(p.Data) > 0 }},
	{fieldMimeType, "mime_type", func(p MessagePart) bool { return p.MimeType != "" }},
	{fieldFileID, "file_id", func(p MessagePart) bool { return p.FileID != "" }},
	{fieldFilename, "filename", func(p MessagePart) bool { return p.Filename != "" }},
}

// setFields reports which optional fields the part actually carries.
func setFields(part MessagePart) messagePartField {
	var set messagePartField
	for _, spec := range messagePartFieldSpecs {
		if spec.set(part) {
			set |= spec.bit
		}
	}

	return set
}

// fieldNames renders a field set in declaration order for error messages.
func fieldNames(fields messagePartField) string {
	var names []string
	for _, spec := range messagePartFieldSpecs {
		if fields&spec.bit != 0 {
			names = append(names, spec.name)
		}
	}

	return strings.Join(names, ", ")
}

// roleSet is a bit set over Role, letting a rule row state its allowed roles
// as data.
type roleSet uint8

const (
	roleBitSystem roleSet = 1 << iota
	roleBitUser
	roleBitAssistant
	roleBitTool

	// rolesAny is the default for kinds this package does not restrict: the
	// vendor, not schema, decides where text or thinking may appear.
	rolesAny = roleBitSystem | roleBitUser | roleBitAssistant | roleBitTool
)

// roleBits maps a Role to its bit, and doubles as the set of valid roles: an
// unknown role has no bit.
var roleBits = map[Role]roleSet{
	RoleSystem:    roleBitSystem,
	RoleUser:      roleBitUser,
	RoleAssistant: roleBitAssistant,
	RoleTool:      roleBitTool,
}

// roleNames renders a role set in Role declaration order for error messages.
func roleNames(roles roleSet) string {
	var names []string
	for _, role := range []Role{RoleSystem, RoleUser, RoleAssistant, RoleTool} {
		if roles&roleBits[role] != 0 {
			names = append(names, string(role))
		}
	}

	return strings.Join(names, ", ")
}

// messagePartSource is one mutually exclusive origin of a media part, with the
// auxiliary fields that only exist on the wire for that origin.
type messagePartSource struct {
	// field discriminates the source; exactly one source field may be set.
	field messagePartField
	// aux are the fields this source may carry. An auxiliary field of a
	// sibling source has no wire field here, so it is rejected rather than
	// dropped at encode time.
	aux messagePartField
	// required are the auxiliary fields this source cannot go without.
	required messagePartField
}

// messagePartRule is the structural contract of one part kind.
type messagePartRule struct {
	// fields is every optional field the kind may set; anything else present
	// belongs to another kind and is rejected.
	fields messagePartField
	// required are the fields the kind cannot go without, independent of any
	// source choice.
	required messagePartField
	// roles are the message roles the kind may appear on.
	roles roleSet
	// sources, when non-empty, demand exactly one of them be chosen.
	sources []messagePartSource
	// value carries the narrow content checks a field set cannot express. It
	// must never re-filter fields, roles or sources — that is the table's job.
	value func(MessagePart) error
}

// messagePartRules is the single source of truth for the field, role and
// source matrix of every canonical part kind. A kind missing here is
// unsupported.
var messagePartRules = map[MessagePartType]messagePartRule{
	MessagePartText: {
		fields: fieldText,
		roles:  rolesAny,
	},
	MessagePartThinking: {
		fields: fieldThinking,
		roles:  rolesAny,
	},
	MessagePartToolCall: {
		fields:   fieldToolCall,
		required: fieldToolCall,
		roles:    rolesAny,
		value:    validateToolCallPart,
	},
	MessagePartToolResult: {
		fields:   fieldText | fieldToolCallID | fieldIsError,
		required: fieldToolCallID,
		roles:    rolesAny,
	},
	MessagePartImage: {
		fields: fieldURL | fieldData | fieldMimeType,
		roles:  roleBitUser,
		sources: []messagePartSource{
			{field: fieldURL},
			{field: fieldData, aux: fieldMimeType, required: fieldMimeType},
		},
		value: validateImagePart,
	},
	MessagePartFile: {
		fields: fieldURL | fieldData | fieldMimeType | fieldFileID | fieldFilename,
		roles:  roleBitUser,
		sources: []messagePartSource{
			{field: fieldURL},
			{field: fieldData, aux: fieldMimeType | fieldFilename, required: fieldMimeType},
			{field: fieldFileID},
		},
	},
}

// validate checks one part against its rule row. The returned error names the
// kind and the violated rule; the caller prefixes the part index.
func (r messagePartRule) validate(kind MessagePartType, role Role, part MessagePart) error {
	set := setFields(part)
	if extra := set &^ r.fields; extra != 0 {
		return fmt.Errorf("%s must not set %s", kind, fieldNames(extra))
	}
	if missing := r.required &^ set; missing != 0 {
		return fmt.Errorf("%s requires %s", kind, fieldNames(missing))
	}
	if roleBits[role]&r.roles == 0 {
		return fmt.Errorf("%s is only valid on %s messages", kind, roleNames(r.roles))
	}
	if err := r.validateSource(kind, set); err != nil {
		return err
	}
	if r.value != nil {
		return r.value(part)
	}

	return nil
}

// validateSource enforces the exactly-one-source rule and binds auxiliary
// fields to the source that has a wire field for them.
func (r messagePartRule) validateSource(kind MessagePartType, set messagePartField) error {
	if len(r.sources) == 0 {
		return nil
	}

	var chosen messagePartSource
	var all, aux messagePartField
	found := 0
	for _, source := range r.sources {
		all |= source.field
		aux |= source.aux
		if set&source.field != 0 {
			chosen = source
			found++
		}
	}
	if found != 1 {
		return fmt.Errorf("%s requires exactly one of %s", kind, fieldNames(all))
	}
	if stray := set & aux &^ chosen.aux; stray != 0 {
		return fmt.Errorf("%s %s source must not set %s", kind, fieldNames(chosen.field), fieldNames(stray))
	}
	if missing := chosen.required &^ set; missing != 0 {
		return fmt.Errorf("%s %s source requires %s", kind, fieldNames(chosen.field), fieldNames(missing))
	}

	return nil
}

// validateToolCallPart checks the payload the field table can only require to
// be present. ToolCall is non-nil here: the rule row requires it.
func validateToolCallPart(part MessagePart) error {
	if part.ToolCall.ID == "" || part.ToolCall.Name == "" {
		return fmt.Errorf("%s requires id and name", MessagePartToolCall)
	}
	if args := part.ToolCall.Arguments; args != "" && !json.Valid([]byte(args)) {
		return fmt.Errorf("%s arguments are invalid JSON", MessagePartToolCall)
	}

	return nil
}

// validateImagePart narrows the inline media type an image may declare. A
// MimeType only reaches here on the data source; the table already rejects it
// on a url source.
func validateImagePart(part MessagePart) error {
	if part.MimeType != "" && !strings.HasPrefix(part.MimeType, "image/") {
		return fmt.Errorf("%s mime_type %q is not an image/* type", MessagePartImage, part.MimeType)
	}

	return nil
}
