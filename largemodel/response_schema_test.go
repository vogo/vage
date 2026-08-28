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
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

// syntheticProtocol stands in for a future provider codec with no native
// structured-output mapping, so the degrade contract can be pinned without a
// real third Caller.
const syntheticProtocol schema.Protocol = "synthetic-test-protocol"

// TestDegradeResponseSchemaPrompt_NilSchemaNoOp covers the "unset" branch of
// the three mutually exclusive paths: no ResponseSchema means the request
// passes through unchanged, not even cloned.
func TestDegradeResponseSchemaPrompt_NilSchemaNoOp(t *testing.T) {
	req := &Request{
		Model:    "test-model",
		Messages: []schema.Message{schema.NewUserMessage(syntheticProtocol, "hi")},
	}

	out, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err != nil {
		t.Fatalf("DegradeResponseSchemaPrompt: %v", err)
	}

	if out != req {
		t.Error("nil ResponseSchema must return the same *Request, not a copy")
	}
}

// TestDegradeResponseSchemaPrompt_InsertsOneInstruction covers the core
// degrade contract: exactly one deterministic system instruction is injected,
// it carries an equivalent schema, it demands raw JSON, existing system
// content and relative order survive, and the original request is untouched.
func TestDegradeResponseSchemaPrompt_InsertsOneInstruction(t *testing.T) {
	respSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"required":   []string{"answer"},
	}

	original := []schema.Message{
		schema.NewSystemMessage(syntheticProtocol, "s0"),
		schema.NewSystemMessage(syntheticProtocol, "s1"),
		schema.NewUserMessage(syntheticProtocol, "hi"),
	}
	req := &Request{
		Model:          "test-model",
		Messages:       original,
		ResponseSchema: respSchema,
	}

	out, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err != nil {
		t.Fatalf("DegradeResponseSchemaPrompt: %v", err)
	}

	// The original request must be left exactly as it was.
	if len(req.Messages) != 3 || req.ResponseSchema == nil {
		t.Fatalf("original request mutated: %+v", req)
	}

	if len(out.Messages) != 4 {
		t.Fatalf("degraded messages = %d, want 4: %+v", len(out.Messages), out.Messages)
	}

	// Existing system content and order survive, unchanged.
	if got := out.Messages[0].Text(); got != "s0" {
		t.Errorf("messages[0] = %q, want %q", got, "s0")
	}

	if got := out.Messages[1].Text(); got != "s1" {
		t.Errorf("messages[1] = %q, want %q", got, "s1")
	}

	// The framework instruction lands after the leading system block.
	instruction := out.Messages[2]
	if instruction.Role() != schema.RoleSystem {
		t.Fatalf("messages[2].Role() = %q, want system", instruction.Role())
	}

	text := instruction.Text()
	if !strings.Contains(text, "raw JSON") {
		t.Errorf("instruction does not demand raw JSON: %q", text)
	}

	_, encoded, ok := strings.Cut(text, "JSON Schema:\n")
	if !ok {
		t.Fatalf("instruction has no embedded schema block: %q", text)
	}

	var embedded any
	if err := json.Unmarshal([]byte(encoded), &embedded); err != nil {
		t.Fatalf("embedded schema is not valid JSON: %v", err)
	}

	gotJSON, _ := json.Marshal(embedded)
	wantJSON, _ := json.Marshal(respSchema)

	if string(gotJSON) != string(wantJSON) {
		t.Errorf("embedded schema = %s, want %s", gotJSON, wantJSON)
	}

	// The user turn keeps its place after the instruction.
	if got := out.Messages[3].Text(); got != "hi" {
		t.Errorf("messages[3] = %q, want %q", got, "hi")
	}

	// The constraint is now fully expressed as a message; a synthetic codec
	// building its wire request from out.Messages needs nothing else, and
	// re-reading out.ResponseSchema must not resurrect a native field.
	if out.ResponseSchema != nil {
		t.Errorf("degraded request still carries ResponseSchema = %#v, want nil", out.ResponseSchema)
	}
}

// TestDegradeResponseSchemaPrompt_NoLeadingSystem covers a request with no
// system message at all: the instruction becomes the new first message.
func TestDegradeResponseSchemaPrompt_NoLeadingSystem(t *testing.T) {
	req := &Request{
		Model:          "test-model",
		Messages:       []schema.Message{schema.NewUserMessage(syntheticProtocol, "hi")},
		ResponseSchema: map[string]any{"type": "string"},
	}

	out, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err != nil {
		t.Fatalf("DegradeResponseSchemaPrompt: %v", err)
	}

	if len(out.Messages) != 2 {
		t.Fatalf("degraded messages = %d, want 2", len(out.Messages))
	}

	if out.Messages[0].Role() != schema.RoleSystem {
		t.Errorf("messages[0].Role() = %q, want system", out.Messages[0].Role())
	}

	if got := out.Messages[1].Text(); got != "hi" {
		t.Errorf("messages[1] = %q, want %q", got, "hi")
	}
}

// TestDegradeResponseSchemaPrompt_Deterministic pins that the same schema
// always renders the same instruction text, so degrading does not itself
// defeat vage's cache key or vendor prompt caching.
func TestDegradeResponseSchemaPrompt_Deterministic(t *testing.T) {
	respSchema := map[string]any{"type": "object", "properties": map[string]any{"a": 1, "b": 2, "c": 3}}
	req := &Request{ResponseSchema: respSchema}

	out1, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err != nil {
		t.Fatalf("DegradeResponseSchemaPrompt: %v", err)
	}

	out2, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err != nil {
		t.Fatalf("DegradeResponseSchemaPrompt: %v", err)
	}

	if out1.Messages[0].Text() != out2.Messages[0].Text() {
		t.Error("identical ResponseSchema produced different instruction text")
	}
}

// TestDegradeResponseSchemaPrompt_UnencodableSchema covers the guard that
// keeps an under-constrained request from ever reaching the network: a
// schema value json.Marshal cannot encode must fail before any backend call.
func TestDegradeResponseSchemaPrompt_UnencodableSchema(t *testing.T) {
	req := &Request{ResponseSchema: make(chan int)}

	_, err := DegradeResponseSchemaPrompt(syntheticProtocol, req)
	if err == nil {
		t.Fatal("expected an error for an unencodable ResponseSchema")
	}
}
