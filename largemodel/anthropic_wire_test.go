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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/vage/schema"
)

// These tests assert the Anthropic wire bytes vage actually puts on the
// network, rather than what the caller believes it sent. The Messages API
// enforces structural rules the vage-side message list does not — most
// importantly that all tool_result blocks answering one assistant turn travel
// in a single user message — so the only way to catch a violation before
// production is to inspect the encoded request.

// wireMessage is one message as Anthropic receives it, decoded far enough to
// assert role and block order.
type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

// wireBlock is one content block, carrying the fields the tests assert on.
type wireBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

// wireRequest is the subset of the Messages request body under test.
type wireRequest struct {
	Messages []wireMessage `json:"messages"`
}

// captureAnthropicWire runs one Call against a server that records the request
// body and replies with a minimal end_turn message, returning the decoded wire
// request.
func captureAnthropicWire(t *testing.T, msgs []schema.Message) wireRequest {
	t.Helper()

	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant",
			"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	caller := newAnthropicCaller(t, srv.URL)

	if _, err := caller.Call(context.Background(), &Request{
		Model:    "test-model",
		Messages: msgs,
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	var got wireRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}

	return got
}

// TestAnthropicWire_ParallelToolResultsMerge pins the fix for the Messages API
// constraint that breaks parallel tool calls: vage records one message per tool
// result, but Anthropic rejects a second tool_result message with "tool_result
// block(s) provided when previous message does not contain any tool_use
// blocks". The two results must therefore leave as one user message, with the
// blocks still in call order.
func TestAnthropicWire_ParallelToolResultsMerge(t *testing.T) {
	proto := schema.ProtocolAnthropicMessages

	got := captureAnthropicWire(t, []schema.Message{
		schema.NewUserMessage(proto, "do two things"),
		schema.NewAssistantTurn(proto, "", "", []schema.ToolCall{
			{ID: "t1", Name: "a", Arguments: `{}`},
			{ID: "t2", Name: "b", Arguments: `{}`},
		}),
		schema.NewToolResultMessage(proto, "t1", "r1", false),
		schema.NewToolResultMessage(proto, "t2", "r2", false),
	})

	// Three messages, not four: the two tool_result messages must have been
	// joined into the single user turn that answers the assistant.
	if len(got.Messages) != 3 {
		t.Fatalf("wire messages = %d, want 3: %+v", len(got.Messages), got.Messages)
	}

	if got.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].role = %q, want assistant", got.Messages[1].Role)
	}

	results := got.Messages[2]
	if results.Role != "user" {
		t.Errorf("messages[2].role = %q, want user", results.Role)
	}

	if len(results.Content) != 2 {
		t.Fatalf("tool_result blocks = %d, want 2: %+v", len(results.Content), results.Content)
	}

	// Block order has to match call order: Anthropic correlates by
	// tool_use_id, but a reordered result set would still mis-pair anything
	// downstream that reads positionally.
	for i, want := range []string{"t1", "t2"} {
		block := results.Content[i]

		if block.Type != "tool_result" {
			t.Errorf("block %d type = %q, want tool_result", i, block.Type)
		}

		if block.ToolUseID != want {
			t.Errorf("block %d tool_use_id = %q, want %q", i, block.ToolUseID, want)
		}
	}
}

// TestAnthropicWire_ToolResultsNotMergedAcrossTurns guards the merge from
// over-reaching: results belonging to different assistant turns are separated
// by that assistant message, and joining across it would corrupt the
// conversation.
func TestAnthropicWire_ToolResultsNotMergedAcrossTurns(t *testing.T) {
	proto := schema.ProtocolAnthropicMessages

	got := captureAnthropicWire(t, []schema.Message{
		schema.NewUserMessage(proto, "start"),
		schema.NewAssistantTurn(proto, "", "", []schema.ToolCall{{ID: "t1", Name: "a", Arguments: `{}`}}),
		schema.NewToolResultMessage(proto, "t1", "r1", false),
		schema.NewAssistantTurn(proto, "", "", []schema.ToolCall{{ID: "t2", Name: "b", Arguments: `{}`}}),
		schema.NewToolResultMessage(proto, "t2", "r2", false),
	})

	if len(got.Messages) != 5 {
		t.Fatalf("wire messages = %d, want 5: %+v", len(got.Messages), got.Messages)
	}

	for _, i := range []int{2, 4} {
		if len(got.Messages[i].Content) != 1 {
			t.Errorf("messages[%d] blocks = %d, want 1", i, len(got.Messages[i].Content))
		}
	}
}

// TestAnthropicWire_SystemHoistedOutOfMessages pins the other structural
// difference from OpenAI: the Messages API has no system role, so system text
// must leave the message list entirely.
func TestAnthropicWire_SystemHoistedOutOfMessages(t *testing.T) {
	proto := schema.ProtocolAnthropicMessages

	got := captureAnthropicWire(t, []schema.Message{
		schema.NewSystemMessage(proto, "be brief"),
		schema.NewUserMessage(proto, "hi"),
	})

	if len(got.Messages) != 1 {
		t.Fatalf("wire messages = %d, want 1: %+v", len(got.Messages), got.Messages)
	}

	if got.Messages[0].Role != "user" {
		t.Errorf("messages[0].role = %q, want user", got.Messages[0].Role)
	}
}

func TestAnthropicWire_ToolErrorPreserved(t *testing.T) {
	proto := schema.ProtocolAnthropicMessages
	got := captureAnthropicWire(t, []schema.Message{
		schema.NewUserMessage(proto, "run it"),
		schema.NewAssistantTurn(proto, "", "", []schema.ToolCall{{
			ID: "t1", Name: "run", Arguments: `{}`,
		}}),
		schema.NewToolResultMessage(proto, "t1", "failed", true),
	})

	if len(got.Messages) != 3 || len(got.Messages[2].Content) != 1 {
		t.Fatalf("unexpected wire messages: %+v", got.Messages)
	}
	if !got.Messages[2].Content[0].IsError {
		t.Fatal("tool result omitted is_error=true")
	}
}

// TestAnthropicStream_MidStreamErrorEvent covers the failure mode that has no
// transport signal at all: Anthropic answers 200, streams part of a turn, then
// delivers an `error` event. The event arrives as an ordinary stream event, so
// unless the decoder fails the stream the consumer sees a truncated reply as a
// complete one and retry, circuit-breaking and budgets never learn of it.
func TestAnthropicStream_MidStreamErrorEvent(t *testing.T) {
	body := sse(
		"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n",
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n",
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`+"\n\n",
		"event: error\ndata: "+`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`+"\n\n",
	)

	caller := newAnthropicCaller(t, sseServer(t, body).URL)

	stream, err := caller.CallStream(context.Background(), simpleRequest(schema.ProtocolAnthropicMessages))
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	var recvErr error

	for {
		if _, recvErr = stream.Recv(); recvErr != nil {
			break
		}
	}

	if errors.Is(recvErr, io.EOF) {
		t.Fatal("stream ended in io.EOF; a mid-stream error event was silently dropped")
	}

	var apiErr *APIError
	if !errors.As(recvErr, &apiErr) {
		t.Fatalf("Recv error = %v (%T), want *APIError", recvErr, recvErr)
	}

	// 529 is what the same overload carries as a non-2xx response, and it is
	// what retry and the circuit breaker judge on.
	if apiErr.StatusCode != statusOverloaded {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, statusOverloaded)
	}

	if apiErr.Type != "overloaded_error" {
		t.Errorf("Type = %q, want %q", apiErr.Type, "overloaded_error")
	}

	if !IsRetryable(recvErr) {
		t.Error("mid-stream overload is not retryable; retry middleware would not re-issue the call")
	}
}

// TestAnthropicStream_MidStreamErrorStatusMapping pins the rest of the
// error-type-to-status mapping, since a mid-stream failure carries no HTTP
// status of its own and the governance middlewares judge only on the derived
// one.
func TestAnthropicStream_MidStreamErrorStatusMapping(t *testing.T) {
	cases := []struct {
		errType string
		want    int
	}{
		{"invalid_request_error", http.StatusBadRequest},
		{"authentication_error", http.StatusUnauthorized},
		{"permission_error", http.StatusForbidden},
		{"not_found_error", http.StatusNotFound},
		{"request_too_large", http.StatusRequestEntityTooLarge},
		{"rate_limit_error", http.StatusTooManyRequests},
		{"timeout_error", http.StatusRequestTimeout},
		{"overloaded_error", statusOverloaded},
		{"api_error", http.StatusInternalServerError},
		{"some_future_error", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.errType, func(t *testing.T) {
			if got := anthropicErrorStatus(tc.errType); got != tc.want {
				t.Errorf("anthropicErrorStatus(%q) = %d, want %d", tc.errType, got, tc.want)
			}
		})
	}
}
