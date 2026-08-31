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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// These tests pin the Messages codec directly: the request bytes it puts on
// the wire, the structural differences it absorbs (hoisted system text, merged
// tool results, mandatory max_tokens), the accounting it derives, and the
// block-oriented stream state it maintains. They are the regression fence for
// the migration of this logic out of the largemodel root package.

// codecRequest is a minimal canonical request carrying one user turn.
func codecRequest(t *testing.T) *modelcore.Request {
	t.Helper()

	return &modelcore.Request{
		Model:    "claude-x",
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi")},
	}
}

func TestBuildMessagesRequest_DefaultMaxTokens(t *testing.T) {
	wire, err := buildMessagesRequest(codecRequest(t))
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	// Anthropic rejects a request without max_tokens, so an unset cap has to
	// become a concrete default rather than an omitted field.
	if wire.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want the %d default", wire.MaxTokens, defaultMaxTokens)
	}
}

func TestBuildMessagesRequest_ExplicitMaxTokensWins(t *testing.T) {
	maxTokens := 99
	req := codecRequest(t)
	req.MaxTokens = &maxTokens
	req.Stop = []string{"</done>"}

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	if wire.MaxTokens != maxTokens {
		t.Errorf("max_tokens = %d, want %d", wire.MaxTokens, maxTokens)
	}

	if len(wire.StopSequences) != 1 || wire.StopSequences[0] != "</done>" {
		t.Errorf("stop_sequences = %v", wire.StopSequences)
	}
}

func TestBuildMessagesRequest_SystemHoistedAndJoined(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{
		schema.NewSystemMessage(schema.ProtocolAnthropicMessages, "first"),
		schema.NewSystemMessage(schema.ProtocolAnthropicMessages, "second"),
		schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi"),
	}

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	// The Messages API has no system message slot: system text belongs to the
	// request field, and consecutive parts are joined rather than dropped.
	if len(wire.Messages) != 1 {
		t.Fatalf("messages = %d, want only the user turn", len(wire.Messages))
	}

	var system string
	if err := json.Unmarshal(wire.System, &system); err != nil {
		t.Fatalf("system field is not a plain string: %s", wire.System)
	}

	if system != "first\n\nsecond" {
		t.Errorf("system = %q, want both parts joined", system)
	}
}

func TestBuildMessagesRequest_PromptCachingBreakpoints(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{
		schema.NewSystemMessage(schema.ProtocolAnthropicMessages, "rules"),
		schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi"),
	}
	req.Tools = []schema.ToolDef{{Name: "a"}, {Name: "b"}}
	req.PromptCaching = true

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	// With caching on, system becomes a block array so it can carry a
	// breakpoint; the plain-string form has nowhere to put one.
	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(wire.System, &blocks); err != nil {
		t.Fatalf("system field is not a block array: %s", wire.System)
	}

	if len(blocks) != 1 || blocks[0].CacheControl == nil {
		t.Fatalf("system blocks = %#v, want one cached text block", blocks)
	}

	// Only the last tool carries the breakpoint: it caches the whole tool
	// block across ReAct iterations.
	if wire.Tools[0].CacheControl != nil || wire.Tools[1].CacheControl == nil {
		t.Errorf("tool cache breakpoints = %v / %v, want it on the last tool only",
			wire.Tools[0].CacheControl, wire.Tools[1].CacheControl)
	}
}

func TestBuildMessagesRequest_NoCachingSendsPlainSystemString(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{
		schema.NewSystemMessage(schema.ProtocolAnthropicMessages, "rules"),
		schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi"),
	}

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	if len(wire.System) == 0 || wire.System[0] != '"' {
		t.Errorf("system = %s, want a plain JSON string", wire.System)
	}
}

func TestBuildMessagesRequest_ForcedToolChoice(t *testing.T) {
	req := codecRequest(t)
	req.Tools = []schema.ToolDef{{Name: "a"}, {Name: "b", ForceUse: true}}

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	if wire.ToolChoice == nil ||
		wire.ToolChoice.Type != anthropic.ToolChoiceTypeTool ||
		wire.ToolChoice.Name != "b" {
		t.Errorf("tool_choice = %#v, want the forced tool", wire.ToolChoice)
	}
}

func TestBuildMessagesRequest_ParallelToolResultsMerge(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{
		schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi"),
		schema.NewAssistantTurn(schema.ProtocolAnthropicMessages, "", "", []schema.ToolCall{
			{ID: "t1", Name: "f", Arguments: "{}"},
			{ID: "t2", Name: "g", Arguments: "{}"},
		}),
		schema.NewToolResultMessage(schema.ProtocolAnthropicMessages, "t1", "one", false),
		schema.NewToolResultMessage(schema.ProtocolAnthropicMessages, "t2", "two", false),
	}

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	// The Messages API accepts parallel results only as one user message
	// following the assistant turn that asked for them.
	if len(wire.Messages) != 3 {
		t.Fatalf("messages = %d, want user / assistant / merged results", len(wire.Messages))
	}

	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(wire.Messages[2].Content, &blocks); err != nil {
		t.Fatalf("merged content is not a block array: %s", wire.Messages[2].Content)
	}

	if len(blocks) != 2 {
		t.Errorf("merged tool_result blocks = %d, want 2", len(blocks))
	}
}

func TestBuildMessagesRequest_ResponseSchemaKeepsOtherOutputConfig(t *testing.T) {
	respSchema := map[string]any{"type": "object"}
	req := codecRequest(t)
	req.ResponseSchema = respSchema

	wire, err := buildMessagesRequest(req)
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	if wire.OutputConfig == nil || wire.OutputConfig.Format == nil {
		t.Fatalf("output_config = %#v, want a json_schema format", wire.OutputConfig)
	}

	if wire.OutputConfig.Format.Type != "json_schema" {
		t.Errorf("output_config.format.type = %q, want json_schema", wire.OutputConfig.Format.Type)
	}

	if fmt.Sprintf("%v", wire.OutputConfig.Format.Schema) != fmt.Sprintf("%v", respSchema) {
		t.Errorf("output_config.format.schema = %v, want the caller's schema unmodified",
			wire.OutputConfig.Format.Schema)
	}
}

func TestBuildMessagesRequest_UnsetResponseSchemaSendsNoOutputConfig(t *testing.T) {
	wire, err := buildMessagesRequest(codecRequest(t))
	if err != nil {
		t.Fatalf("buildMessagesRequest: %v", err)
	}

	if wire.OutputConfig != nil {
		t.Errorf("output_config = %#v, want unset", wire.OutputConfig)
	}
}

func TestBuildMessagesRequest_RejectsForeignProtocol(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}

	if _, err := buildMessagesRequest(req); err == nil {
		t.Fatal("buildMessagesRequest accepted a message recorded under another protocol")
	}
}

func TestMessagesFinishReason(t *testing.T) {
	cases := map[string]string{
		anthropic.StopReasonEndTurn:      "stop",
		anthropic.StopReasonStopSequence: "stop",
		anthropic.StopReasonMaxTokens:    "length",
		anthropic.StopReasonToolUse:      "tool_calls",
		// No cross-vendor equivalent: passed through so callers can see it.
		"refusal": "refusal",
		"":        "",
	}

	for reason, want := range cases {
		if got := messagesFinishReason(reason); got != want {
			t.Errorf("messagesFinishReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestMessagesUsage_PromptIncludesCacheTokens(t *testing.T) {
	usage := messagesUsage(&anthropic.MessagesUsage{
		InputTokens:              10,
		OutputTokens:             5,
		CacheCreationInputTokens: 3,
		CacheReadInputTokens:     7,
		ServiceTier:              "standard",
		OutputTokensDetails:      &anthropic.OutputTokensDetails{ThinkingTokens: 2},
	})

	// Anthropic reports input tokens exclusive of cached ones; vage's budgets
	// assume OpenAI's inclusive meaning, so the three are summed.
	want := schema.Usage{
		PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25,
		CacheReadTokens: 7, CacheWriteTokens: 3, ReasoningTokens: 2, ServiceTier: "standard",
	}

	if usage != want {
		t.Errorf("usage = %+v, want %+v", usage, want)
	}
}

func TestMessagesUsage_Nil(t *testing.T) {
	if usage := messagesUsage(nil); usage != (schema.Usage{}) {
		t.Errorf("usage = %+v, want zero", usage)
	}
}

// TestMessagesErrorStatus pins the error-type-to-status mapping, since a
// mid-stream failure carries no HTTP status of its own and the governance
// middlewares judge only on the derived one.
func TestMessagesErrorStatus(t *testing.T) {
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
			if got := messagesErrorStatus(tc.errType); got != tc.want {
				t.Errorf("messagesErrorStatus(%q) = %d, want %d", tc.errType, got, tc.want)
			}
		})
	}
}

func TestMessagesStreamError_WithoutDetail(t *testing.T) {
	var classified *modelcore.APIError
	if !errors.As(messagesStreamError(nil), &classified) {
		t.Fatal("a detail-less error event must still fail the stream")
	}

	if classified.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", classified.StatusCode)
	}
}

func TestNormalizeHTTPError_ClassifiesVendorFailure(t *testing.T) {
	vendorErr := &anthropic.HTTPError{
		Status: http.StatusTooManyRequests, Type: "rate_limit_error", Message: "slow down",
	}

	var classified *modelcore.APIError
	if !errors.As(normalizeHTTPError(vendorErr), &classified) {
		t.Fatal("vendor HTTP error was not classified")
	}

	if classified.StatusCode != http.StatusTooManyRequests ||
		classified.Type != "rate_limit_error" ||
		classified.Message != "slow down" {
		t.Errorf("classified = %+v", classified)
	}

	if !errors.Is(errors.Unwrap(classified), vendorErr) {
		t.Errorf("unwrap = %v, want the vendor error", errors.Unwrap(classified))
	}
}

func TestNormalizeHTTPError_LeavesNonVendorErrorAlone(t *testing.T) {
	transport := errors.New("dial tcp: connection refused")

	if _, ok := errors.AsType[*modelcore.APIError](normalizeHTTPError(transport)); ok {
		t.Error("a transport failure must not be dressed up as an API error")
	}
}

// newCodecServer serves one fixed JSON body and records the decoded request.
func newCodecServer(t *testing.T, body string, seen *map[string]any) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			decoded := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&decoded)
			*seen = decoded
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
}

// newCodecSSEServer serves fixed SSE frames.
func newCodecSSEServer(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)

			return
		}

		for _, frame := range frames {
			_, _ = fmt.Fprint(w, frame)
			flusher.Flush()
		}
	}))
}

func newCodecForServer(t *testing.T, server *httptest.Server) *MessagesCodec {
	t.Helper()

	return NewMessagesCodec(anthropic.NewClient("test-key",
		anthropic.WithBaseURL(server.URL), anthropic.WithHTTPClient(server.Client())))
}

func TestMessagesCodec_CallNormalizesResponse(t *testing.T) {
	server := newCodecServer(t, `{"id":"msg-1","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"hello"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":4}}`, nil)
	defer server.Close()

	result, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if result.ID != "msg-1" || result.Model != "claude-x" {
		t.Errorf("result = %+v", result)
	}

	if result.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want the normalized stop", result.FinishReason)
	}

	if result.Message.Text() != "hello" || result.Message.Thinking() != "pondering" {
		t.Errorf("message = %q / %q", result.Message.Text(), result.Message.Thinking())
	}

	if result.Usage.PromptTokens != 14 || result.Usage.CacheReadTokens != 4 {
		t.Errorf("usage = %+v, want cache tokens folded into the prompt total", result.Usage)
	}

	// The assistant turn keeps the vendor's own blocks so it can be replayed
	// verbatim on the next iteration.
	if len(result.Message.Origin()) == 0 {
		t.Error("assistant message carries no origin payload; the turn is no longer replayable")
	}
}

func TestMessagesCodec_CallParallelToolCalls(t *testing.T) {
	server := newCodecServer(t, `{"id":"msg-2","type":"message","role":"assistant","model":"claude-x",
		"content":[
			{"type":"tool_use","id":"t1","name":"f","input":{"x":1}},
			{"type":"tool_use","id":"t2","name":"g","input":{"y":2}}],
		"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, nil)
	defer server.Close()

	result, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	calls := result.Message.ToolCalls()
	if len(calls) != 2 || calls[0].ID != "t1" || calls[1].ID != "t2" {
		t.Fatalf("tool calls = %#v", calls)
	}

	if result.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", result.FinishReason)
	}
}

func TestMessagesCodec_CallEmptyResponse(t *testing.T) {
	server := newCodecServer(t, `{"id":"msg-3","type":"message","role":"assistant","model":"claude-x",
		"content":[],"stop_reason":""}`, nil)
	defer server.Close()

	_, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if !errors.Is(err, modelcore.ErrEmptyResponse) {
		t.Errorf("err = %v, want ErrEmptyResponse", err)
	}
}

func TestMessagesCodec_StreamDecodesBlocksAndUsage(t *testing.T) {
	server := newCodecSSEServer(
		t,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"he\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"f\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"g\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"y\\\":2}\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":1}\"}}\n\n",
		"event: some_future_event\ndata: {\"type\":\"some_future_event\"}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	defer server.Close()

	stream, err := newCodecForServer(t, server).CallStream(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	defer func() { _ = stream.Close() }()

	var (
		text     string
		thinking string
		finish   string
		usage    *schema.Usage
	)

	names := map[int]string{}
	args := map[int]string{}

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}

		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}

		text += chunk.TextDelta
		thinking += chunk.ThinkingDelta

		for _, delta := range chunk.ToolCallDeltas {
			if delta.Name != "" {
				names[delta.Index] = delta.Name
			}

			args[delta.Index] += delta.ArgumentsDelta
		}

		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}

		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	if text != "he" || thinking != "hmm" {
		t.Errorf("text/thinking = %q / %q", text, thinking)
	}

	// Content-block indices 1 and 2 become tool ordinals 0 and 1, and each
	// block's partial JSON has to reach its own call even though the deltas
	// arrive out of block order.
	if names[0] != "f" || names[1] != "g" {
		t.Errorf("tool names by ordinal = %#v", names)
	}

	if args[0] != `{"x":1}` || args[1] != `{"y":2}` {
		t.Errorf("tool arguments by ordinal = %#v", args)
	}

	if finish != "tool_calls" {
		t.Errorf("finish reason = %q", finish)
	}

	// message_delta reports output tokens alone; the merged snapshot is the
	// complete accounting for the turn.
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 7 {
		t.Errorf("terminal usage = %+v", usage)
	}
}

func TestMessagesCodec_StreamFailsOnErrorEvent(t *testing.T) {
	server := newCodecSSEServer(
		t,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n",
	)
	defer server.Close()

	stream, err := newCodecForServer(t, server).CallStream(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	defer func() { _ = stream.Close() }()

	var recvErr error

	for {
		chunk, err := stream.Recv()
		if err != nil {
			recvErr = err

			break
		}

		if chunk == nil {
			t.Fatal("Recv returned no chunk and no error")
		}
	}

	// An error event arrives as an ordinary event over a 200 response. Letting
	// it end the stream as io.EOF would pass a truncated turn off as a
	// complete one.
	if errors.Is(recvErr, io.EOF) {
		t.Fatal("mid-stream error event ended the stream as a normal completion")
	}

	var classified *modelcore.APIError
	if !errors.As(recvErr, &classified) {
		t.Fatalf("err = %v, want a classified API error", recvErr)
	}

	if classified.StatusCode != statusOverloaded {
		t.Errorf("status = %d, want %d derived from the error type", classified.StatusCode, statusOverloaded)
	}
}

func TestMessagesCodec_StreamClassifiesOpenFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	}))
	defer server.Close()

	_, err := newCodecForServer(t, server).CallStream(context.Background(), codecRequest(t))

	var classified *modelcore.APIError
	if !errors.As(err, &classified) || classified.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v, want a classified 400", err)
	}
}

func TestMessagesCodec_EncodeFailureSkipsBackend(t *testing.T) {
	var reached bool

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer server.Close()

	// A message recorded under another protocol is rejected during request
	// assembly, before any network I/O.
	req := codecRequest(t)
	req.Messages = []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}

	if _, err := newCodecForServer(t, server).Call(context.Background(), req); err == nil {
		t.Fatal("Call accepted a message recorded under another protocol")
	}

	if reached {
		t.Error("the backend was called despite a request-build failure")
	}
}
