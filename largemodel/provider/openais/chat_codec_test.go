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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// These tests pin the Chat Completions codec directly: the request bytes it
// puts on the wire, the accounting and finish reasons it derives, and the
// failures it classifies. They are the regression fence for the migration of
// this logic out of the largemodel root package — "the code moved" must not be
// allowed to hide "the wire changed".

// codecUserMessage is one canonical user turn recorded under this protocol.
func codecUserMessage(t *testing.T, text string) schema.Message {
	t.Helper()

	return schema.NewUserMessage(schema.ProtocolOpenAIChat, text)
}

// codecRequest is a minimal canonical request carrying one user turn.
func codecRequest(t *testing.T) *modelcore.Request {
	t.Helper()

	return &modelcore.Request{
		Model:    "gpt-4",
		Messages: []schema.Message{codecUserMessage(t, "hi")},
	}
}

func TestBuildChatRequest_ParameterMapping(t *testing.T) {
	maxTokens := 256
	temperature := 0.3
	req := codecRequest(t)
	req.MaxTokens = &maxTokens
	req.Temperature = &temperature
	req.Stop = []string{"</done>"}

	wire, err := buildChatRequest(req)
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	if wire.MaxCompletionTokens == nil || *wire.MaxCompletionTokens != maxTokens {
		t.Errorf("max_completion_tokens = %v, want %d", wire.MaxCompletionTokens, maxTokens)
	}

	// max_tokens is deprecated and rejected by reasoning models, so the cap
	// must never land there.
	if wire.MaxTokens != nil {
		t.Errorf("max_tokens = %v, want unset", *wire.MaxTokens)
	}

	if wire.Temperature == nil || *wire.Temperature != temperature {
		t.Errorf("temperature = %v, want %v", wire.Temperature, temperature)
	}

	if len(wire.Stop) != 1 || wire.Stop[0] != "</done>" {
		t.Errorf("stop = %v, want [</done>]", wire.Stop)
	}

	if len(wire.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(wire.Messages))
	}
}

func TestBuildChatRequest_ToolsAndForcedChoice(t *testing.T) {
	req := codecRequest(t)
	req.Tools = []schema.ToolDef{
		{Name: "search", Description: "search the web", Parameters: map[string]any{"type": "object"}},
		{Name: "commit", ForceUse: true},
	}

	wire, err := buildChatRequest(req)
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	if len(wire.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(wire.Tools))
	}

	if wire.Tools[0].Type != openai.ToolTypeFunction || wire.Tools[0].Function.Name != "search" {
		t.Errorf("tools[0] = %#v", wire.Tools[0])
	}

	choice, ok := wire.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %#v, want an object naming the forced tool", wire.ToolChoice)
	}

	fn, _ := choice["function"].(map[string]any)
	if choice["type"] != openai.ToolTypeFunction || fn["name"] != "commit" {
		t.Errorf("tool_choice = %#v, want forced commit", choice)
	}
}

func TestBuildChatRequest_NoForcedToolLeavesChoiceUnset(t *testing.T) {
	req := codecRequest(t)
	req.Tools = []schema.ToolDef{{Name: "search"}}

	wire, err := buildChatRequest(req)
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	if wire.ToolChoice != nil {
		t.Errorf("tool_choice = %#v, want unset so the model decides", wire.ToolChoice)
	}
}

func TestBuildChatRequest_ResponseSchema(t *testing.T) {
	respSchema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}}
	req := codecRequest(t)
	req.ResponseSchema = respSchema

	wire, err := buildChatRequest(req)
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	format, ok := wire.ResponseFormat.(map[string]any)
	if !ok {
		t.Fatalf("response_format = %#v, want a json_schema object", wire.ResponseFormat)
	}

	if format["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", format["type"])
	}

	js, ok := format["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("response_format.json_schema = %#v", format["json_schema"])
	}

	// The name is fixed so identical requests keep the same wire bytes and
	// stay on the same prompt-cache key.
	if js["name"] != "vage_response_schema" {
		t.Errorf("json_schema.name = %v, want vage_response_schema", js["name"])
	}

	if strict, _ := js["strict"].(bool); !strict {
		t.Errorf("json_schema.strict = %v, want true", js["strict"])
	}

	// The caller's schema is forwarded as-is, never rewritten or trimmed.
	if fmt.Sprintf("%v", js["schema"]) != fmt.Sprintf("%v", respSchema) {
		t.Errorf("json_schema.schema = %v, want the caller's schema unmodified", js["schema"])
	}
}

func TestBuildChatRequest_UnsetResponseSchemaSendsNoFormat(t *testing.T) {
	wire, err := buildChatRequest(codecRequest(t))
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	if wire.ResponseFormat != nil {
		t.Errorf("response_format = %#v, want unset", wire.ResponseFormat)
	}
}

func TestBuildChatRequest_RejectsForeignProtocol(t *testing.T) {
	req := codecRequest(t)
	req.Messages = []schema.Message{schema.NewUserMessage(schema.ProtocolAnthropicMessages, "hi")}

	if _, err := buildChatRequest(req); err == nil {
		t.Fatal("buildChatRequest accepted a message recorded under another protocol")
	}
}

func TestBuildChatRequest_MediaEncodesInline(t *testing.T) {
	msg := schema.NewMessage(schema.ProtocolOpenAIChat, schema.RoleUser, []schema.MessagePart{
		{Type: schema.MessagePartText, Text: "what is this"},
		{Type: schema.MessagePartImage, Data: []byte{0x1, 0x2}, MimeType: "image/png"},
	})

	req := codecRequest(t)
	req.Messages = []schema.Message{msg}

	wire, err := buildChatRequest(req)
	if err != nil {
		t.Fatalf("buildChatRequest: %v", err)
	}

	parts := wire.Messages[0].Content.Parts()
	if len(parts) != 2 || parts[1].ImageURL == nil {
		t.Fatalf("content parts = %#v, want text plus an image_url part", parts)
	}
}

func TestChatUsage_Normalization(t *testing.T) {
	usage := chatUsage(&openai.ChatCompletionUsage{
		PromptTokens:            10,
		CompletionTokens:        5,
		TotalTokens:             15,
		PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 4},
		CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: 2},
	}, "scale")

	want := schema.Usage{
		PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		CacheReadTokens: 4, ReasoningTokens: 2, ServiceTier: "scale",
	}

	if usage != want {
		t.Errorf("usage = %+v, want %+v", usage, want)
	}
}

func TestChatUsage_NilKeepsServiceTier(t *testing.T) {
	if usage := chatUsage(nil, "flex"); usage != (schema.Usage{ServiceTier: "flex"}) {
		t.Errorf("usage = %+v, want only the service tier", usage)
	}
}

func TestChatChunk_ParallelToolFragments(t *testing.T) {
	reason := "tool_calls"
	chunk := chatChunk(&openai.ChatCompletionChunk{
		Choices: []openai.ChatCompletionChunkChoice{{
			Delta: openai.ChatCompletionMessage{
				Content:          openai.NewTextContent("hi"),
				ReasoningContent: "because",
				ToolCalls: []openai.ChatCompletionToolCall{
					{Index: 0, ID: "a", Function: openai.ChatCompletionFunctionCall{Name: "f", Arguments: `{"x`}},
					{Index: 1, ID: "b", Function: openai.ChatCompletionFunctionCall{Name: "g", Arguments: `{"y`}},
				},
			},
			FinishReason: &reason,
		}},
	})

	if chunk.TextDelta != "hi" || chunk.ThinkingDelta != "because" {
		t.Errorf("deltas = %q / %q", chunk.TextDelta, chunk.ThinkingDelta)
	}

	if chunk.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", chunk.FinishReason)
	}

	// Both fragments must survive: a chunk carrying two parallel calls that
	// forwarded only one would silently drop a tool invocation.
	if len(chunk.ToolCallDeltas) != 2 ||
		chunk.ToolCallDeltas[0].Index != 0 || chunk.ToolCallDeltas[1].Index != 1 {
		t.Fatalf("tool call deltas = %#v", chunk.ToolCallDeltas)
	}
}

func TestChatChunk_UsageOnlyChunkCarriesNoDelta(t *testing.T) {
	chunk := chatChunk(&openai.ChatCompletionChunk{
		Usage:       &openai.ChatCompletionUsage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
		ServiceTier: "scale",
	})

	if chunk.Usage == nil || chunk.Usage.TotalTokens != 4 || chunk.Usage.ServiceTier != "scale" {
		t.Fatalf("usage = %+v, want the reported totals", chunk.Usage)
	}

	if chunk.TextDelta != "" || len(chunk.ToolCallDeltas) != 0 || chunk.FinishReason != "" {
		t.Errorf("usage-only chunk carried content: %+v", chunk)
	}
}

func TestNormalizeHTTPError_ClassifiesVendorFailure(t *testing.T) {
	vendorErr := &openai.HTTPError{
		Status: http.StatusTooManyRequests, Code: "rate_limit", Type: "rate_limit_error", Message: "slow down",
	}

	var classified *modelcore.APIError
	if !errors.As(normalizeHTTPError(vendorErr), &classified) {
		t.Fatal("vendor HTTP error was not classified")
	}

	if classified.StatusCode != http.StatusTooManyRequests ||
		classified.Code != "rate_limit" ||
		classified.Type != "rate_limit_error" ||
		classified.Message != "slow down" {
		t.Errorf("classified = %+v", classified)
	}

	// The native error stays reachable for callers that need vendor detail.
	if !errors.Is(errors.Unwrap(classified), vendorErr) {
		t.Errorf("unwrap = %v, want the vendor error", errors.Unwrap(classified))
	}
}

func TestNormalizeHTTPError_LeavesNonVendorErrorAlone(t *testing.T) {
	transport := errors.New("dial tcp: connection refused")

	if got := normalizeHTTPError(transport); !errors.Is(got, transport) {
		t.Errorf("normalizeHTTPError = %v, want the original error untouched", got)
	}

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

func newCodecForServer(t *testing.T, server *httptest.Server) *ChatCodec {
	t.Helper()

	return NewChatCodec(openai.NewClient("test-key",
		openai.WithBaseURL(server.URL), openai.WithHTTPClient(server.Client())))
}

func TestChatCodec_CallNormalizesResponse(t *testing.T) {
	var seen map[string]any

	server := newCodecServer(t, `{"id":"cmpl-1","model":"gpt-4","service_tier":"scale",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
		"prompt_tokens_details":{"cached_tokens":4}}}`, &seen)
	defer server.Close()

	result, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if result.ID != "cmpl-1" || result.Model != "gpt-4" || result.FinishReason != "stop" {
		t.Errorf("result = %+v", result)
	}

	if result.Usage.PromptTokens != 10 || result.Usage.CacheReadTokens != 4 {
		t.Errorf("usage = %+v", result.Usage)
	}

	if result.Message.Text() != "hello" {
		t.Errorf("message text = %q", result.Message.Text())
	}

	// The assistant turn keeps the vendor's own wire bytes so the next
	// iteration can replay it verbatim.
	if len(result.Message.Origin()) == 0 {
		t.Error("assistant message carries no origin payload; the turn is no longer replayable")
	}

	if seen["model"] != "gpt-4" {
		t.Errorf("request model = %v, want gpt-4", seen["model"])
	}
}

func TestChatCodec_CallPassesUnknownFinishReasonThrough(t *testing.T) {
	server := newCodecServer(t, `{"id":"c","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"future_reason"}]}`, nil)
	defer server.Close()

	result, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if result.FinishReason != "future_reason" {
		t.Errorf("finish reason = %q, want it passed through verbatim", result.FinishReason)
	}
}

func TestChatCodec_CallEmptyChoices(t *testing.T) {
	server := newCodecServer(t, `{"id":"c","model":"m","choices":[]}`, nil)
	defer server.Close()

	_, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))
	if !errors.Is(err, modelcore.ErrEmptyResponse) {
		t.Errorf("err = %v, want ErrEmptyResponse", err)
	}
}

func TestChatCodec_CallClassifiesHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit"}}`)
	}))
	defer server.Close()

	_, err := newCodecForServer(t, server).Call(context.Background(), codecRequest(t))

	var classified *modelcore.APIError
	if !errors.As(err, &classified) {
		t.Fatalf("err = %v, want a classified API error", err)
	}

	if classified.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", classified.StatusCode)
	}
}

func TestChatCodec_CallStreamMergesInterleavedToolFragments(t *testing.T) {
	server := newCodecSSEServer(
		t,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"g\",\"arguments\":\"{\\\"y\\\":\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n",
		"data: [DONE]\n\n",
	)
	defer server.Close()

	stream, err := newCodecForServer(t, server).CallStream(context.Background(), codecRequest(t))
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	defer func() { _ = stream.Close() }()

	args := map[int]string{}

	var (
		finish string
		usage  *schema.Usage
	)

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}

		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}

		for _, delta := range chunk.ToolCallDeltas {
			args[delta.Index] += delta.ArgumentsDelta
		}

		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}

		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	if args[0] != `{"x":1}` || args[1] != `{"y":` {
		t.Errorf("merged arguments = %#v", args)
	}

	if finish != "tool_calls" {
		t.Errorf("finish reason = %q", finish)
	}

	if usage == nil || usage.TotalTokens != 5 {
		t.Errorf("terminal usage = %+v", usage)
	}
}

func TestChatCodec_CallStreamClassifiesOpenFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	_, err := newCodecForServer(t, server).CallStream(context.Background(), codecRequest(t))

	var classified *modelcore.APIError
	if !errors.As(err, &classified) || classified.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v, want a classified 400", err)
	}
}

func TestChatCodec_EncodeFailureSkipsBackend(t *testing.T) {
	var reached bool

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer server.Close()

	// A file part with neither a file id nor a filename cannot be expressed on
	// this wire, so it must fail during encoding rather than as a backend 4xx.
	msg := schema.NewMessage(schema.ProtocolOpenAIChat, schema.RoleUser, []schema.MessagePart{
		{Type: schema.MessagePartFile, URL: "https://example.com/a.pdf"},
	})

	req := codecRequest(t)
	req.Messages = []schema.Message{msg}

	if _, err := newCodecForServer(t, server).Call(context.Background(), req); err == nil {
		t.Fatal("Call accepted a message this wire cannot express")
	}

	if reached {
		t.Error("the backend was called despite an encoding failure")
	}
}
