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

package largemodel

import (
	"encoding/json"
	"fmt"

	"github.com/vogo/vage/schema"
)

// DegradeResponseSchemaPrompt is the fallback path for a protocol codec with
// no native structured-output mapping (openai_chat.go and
// anthropic_messages.go instead send req.ResponseSchema as a vendor-native
// field and never call this). It returns req unchanged when ResponseSchema is
// nil; otherwise it returns a clone carrying one additional deterministic
// system instruction that asks the model for raw JSON matching the schema,
// with ResponseSchema cleared on the clone since the constraint is now fully
// expressed as a message.
//
// The instruction is inserted right after any messages already in the system
// role, preserving their relative order and content; req and its Messages are
// never mutated. The same schema always renders the same instruction text, so
// degrading does not itself break prompt caching or vage's own cache key.
//
// It fails before any network call when ResponseSchema cannot be encoded as
// JSON, rather than silently sending a request with a weaker guarantee.
func DegradeResponseSchemaPrompt(proto schema.Protocol, req *Request) (*Request, error) {
	if req.ResponseSchema == nil {
		return req, nil
	}

	instruction, err := responseSchemaInstruction(req.ResponseSchema)
	if err != nil {
		return nil, err
	}

	clone := req.Clone()
	clone.ResponseSchema = nil

	insertAt := 0
	for insertAt < len(clone.Messages) && clone.Messages[insertAt].Role() == schema.RoleSystem {
		insertAt++
	}

	messages := make([]schema.Message, 0, len(clone.Messages)+1)
	messages = append(messages, clone.Messages[:insertAt]...)
	messages = append(messages, schema.NewSystemMessage(proto, instruction))
	messages = append(messages, clone.Messages[insertAt:]...)
	clone.Messages = messages

	return clone, nil
}

// responseSchemaInstruction renders the framework-owned degrade instruction.
// It is deterministic in the caller-supplied schema alone (json.Marshal
// serializes map keys in sorted order), so the same schema always produces
// byte-identical text.
func responseSchemaInstruction(respSchema any) (string, error) {
	encoded, err := json.Marshal(respSchema)
	if err != nil {
		return "", fmt.Errorf("vage: encode response schema for prompt degrade: %w", err)
	}

	return fmt.Sprintf(
		"Respond with a single JSON value that matches the following JSON Schema exactly. "+
			"Output raw JSON only: no code fences, no surrounding prose.\n\nJSON Schema:\n%s",
		encoded,
	), nil
}
