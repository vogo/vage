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
	"errors"
	"fmt"
)

// Protocol identifies which vendor wire protocol a model call speaks. vage
// binds every model to exactly one protocol at configuration time, so the
// call layer always knows which native aimodel client and which native wire
// types are in play — vage keeps no vendor-neutral request type to dispatch
// on.
//
// Protocol also tags every persisted message: a Message stores the wire form
// of the vendor that produced it, so replaying a session requires the same
// protocol that recorded it.
type Protocol string

// The protocols vage speaks. Each implemented one maps to a native aimodel
// client: ProtocolOpenAIChat to openai.Client.ChatCompletions and
// ProtocolAnthropicMessages to anthropic.Client.Messages.
//
// ProtocolOpenAIResponses names OpenAI's Responses API, which vage does not
// implement yet: largemodel ships no Caller for it, so Valid rejects it rather
// than letting a configuration pass validation and then find no caller to run
// on. The constant is kept so the wire form is already named when the caller
// lands.
const (
	ProtocolOpenAIChat        Protocol = "openai-chat"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
)

// ErrUnknownProtocol reports a Protocol value vage does not implement. It is
// returned at configuration time, before any network I/O.
var ErrUnknownProtocol = errors.New("vage: unknown model protocol")

// ErrProtocolMismatch reports an attempt to read a message in a protocol
// other than the one that recorded it. Because vage stores native vendor wire
// forms, a session recorded against one protocol cannot be replayed against
// another.
var ErrProtocolMismatch = errors.New("vage: message protocol mismatch")

// Valid reports whether p is a protocol vage implements — that is, one a
// largemodel Caller can actually speak. A protocol that is merely named, such
// as ProtocolOpenAIResponses, is not valid until its caller exists.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolOpenAIChat, ProtocolAnthropicMessages:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (p Protocol) String() string { return string(p) }

// Validate returns ErrUnknownProtocol wrapped with the offending value when p
// is not a protocol vage implements.
func (p Protocol) Validate() error {
	if !p.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownProtocol, string(p))
	}

	return nil
}
