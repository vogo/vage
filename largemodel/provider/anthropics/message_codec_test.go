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
	"encoding/json"
	"testing"

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
