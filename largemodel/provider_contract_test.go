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
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

// These tests drive both protocol callers against local HTTP/SSE servers, so
// the vendor wire mapping is exercised end to end without real credentials.
// They are the contract tests for the dual-track design: every behaviour vage
// depends on — text, tool calls, usage accounting, error classification — has
// to hold for OpenAI and Anthropic alike, even though the wire shapes differ.

// callerFactory builds a caller pointed at a test server.
type callerFactory func(t *testing.T, baseURL string) Caller

// providerCase is one protocol under test, with the vendor-specific response
// bodies that produce a common expected outcome.
type providerCase struct {
	name     string
	protocol schema.Protocol
	newCall  callerFactory

	// textBody is a non-streaming reply whose assistant text is "hello".
	textBody string

	// toolBody is a non-streaming reply requesting get_weather({"city":"SF"}).
	toolBody string

	// streamBody is an SSE stream emitting "he" then "llo", then finishing
	// with usage totalling 10 prompt / 5 completion tokens.
	streamBody string

	// toolStreamBody is an SSE stream emitting one tool call whose arguments
	// arrive split across two deltas.
	toolStreamBody string
}

func newOpenAICaller(t *testing.T, baseURL string) Caller {
	t.Helper()

	c, err := NewOpenAIChatCaller("test-key", baseURL, fastRouting())
	if err != nil {
		t.Fatalf("NewOpenAIChatCaller: %v", err)
	}

	return c
}

func newAnthropicCaller(t *testing.T, baseURL string) Caller {
	t.Helper()

	c, err := NewAnthropicMessagesCaller("test-key", baseURL, fastRouting())
	if err != nil {
		t.Fatalf("NewAnthropicMessagesCaller: %v", err)
	}

	return c
}

// sse joins pre-formatted SSE frames.
func sse(frames ...string) string { return strings.Join(frames, "") }

func providerCases() []providerCase {
	return []providerCase{
		{
			name:     "openai",
			protocol: schema.ProtocolOpenAIChat,
			newCall:  newOpenAICaller,
			textBody: `{"id":"cmpl-1","object":"chat.completion","model":"gpt-4",
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
				"prompt_tokens_details":{"cached_tokens":4},
				"completion_tokens_details":{"reasoning_tokens":2}}}`,
			toolBody: `{"id":"cmpl-2","object":"chat.completion","model":"gpt-4",
				"choices":[{"index":0,"message":{"role":"assistant","content":"",
				"tool_calls":[{"index":0,"id":"call-1","type":"function",
				"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},
				"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			streamBody: sse(
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`+"\n\n",
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`+"\n\n",
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n",
				"data: [DONE]\n\n",
			),
			toolStreamBody: sse(
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`+"\n\n",
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":null}]}`+"\n\n",
				"data: "+`{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n",
				"data: [DONE]\n\n",
			),
		},
		{
			name:     "anthropic",
			protocol: schema.ProtocolAnthropicMessages,
			newCall:  newAnthropicCaller,
			// Anthropic reports input tokens exclusive of cache; the caller
			// sums fresh + cache-write + cache-read into PromptTokens, so
			// 4 + 2 + 4 = 10 matches OpenAI's 10 prompt tokens.
			textBody: `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",
				"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
				"usage":{"input_tokens":4,"output_tokens":5,"cache_creation_input_tokens":2,
				"cache_read_input_tokens":4,"output_tokens_details":{"thinking_tokens":2}}}`,
			toolBody: `{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5",
				"content":[{"type":"tool_use","id":"call-1","name":"get_weather","input":{"city":"SF"}}],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":10,"output_tokens":5}}`,
			streamBody: sse(
				"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n",
				"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n",
				"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"he"}}`+"\n\n",
				"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"llo"}}`+"\n\n",
				"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`+"\n\n",
				"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n",
			),
			toolStreamBody: sse(
				"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n",
				"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"get_weather"}}`+"\n\n",
				"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`+"\n\n",
				"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"SF\"}"}}`+"\n\n",
				"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`+"\n\n",
				"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n",
			),
		},
	}
}

// jsonServer replies to every request with status and body.
func jsonServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// sseServer streams body as an event stream.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// simpleRequest is a one-turn request in the given protocol.
func simpleRequest(proto schema.Protocol) *Request {
	return &Request{
		Model: "test-model",
		Messages: []schema.Message{
			schema.NewSystemMessage(proto, "be brief"),
			schema.NewUserMessage(proto, "hi"),
		},
	}
}

// TestProviderCall_Text covers a plain text completion on both providers,
// including the usage normalization that budgets and metrics depend on.
func TestProviderCall_Text(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, jsonServer(t, http.StatusOK, pc.textBody).URL)

			resp, err := caller.Call(context.Background(), simpleRequest(pc.protocol))
			if err != nil {
				t.Fatalf("Call: %v", err)
			}

			if caller.Protocol() != pc.protocol {
				t.Errorf("Protocol() = %q, want %q", caller.Protocol(), pc.protocol)
			}

			if got := resp.Message.Text(); got != "hello" {
				t.Errorf("Text() = %q, want %q", got, "hello")
			}

			if resp.Message.Protocol != pc.protocol {
				t.Errorf("message protocol = %q, want %q", resp.Message.Protocol, pc.protocol)
			}

			if resp.FinishReason != FinishReasonStop {
				t.Errorf("FinishReason = %q, want %q", resp.FinishReason, FinishReasonStop)
			}

			// Both vendors must land on the same normalized accounting.
			want := schema.Usage{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
				CacheReadTokens: 4, ReasoningTokens: 2,
			}

			got := resp.Usage
			got.CacheWriteTokens = 0 // vendor-specific; asserted separately below
			got.ServiceTier = ""

			if got != want {
				t.Errorf("Usage = %+v, want %+v", got, want)
			}
		})
	}
}

// TestProviderCall_AnthropicCacheWriteTokens pins the one usage field only
// Anthropic reports, so the cache-write accounting is not silently dropped.
func TestProviderCall_AnthropicCacheWriteTokens(t *testing.T) {
	pc := providerCases()[1]
	caller := pc.newCall(t, jsonServer(t, http.StatusOK, pc.textBody).URL)

	resp, err := caller.Call(context.Background(), simpleRequest(pc.protocol))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if resp.Usage.CacheWriteTokens != 2 {
		t.Errorf("CacheWriteTokens = %d, want 2", resp.Usage.CacheWriteTokens)
	}
}

// TestProviderCall_ToolCall covers a tool-calling turn on both providers,
// including the flattening of each vendor's very different tool shape.
func TestProviderCall_ToolCall(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, jsonServer(t, http.StatusOK, pc.toolBody).URL)

			req := simpleRequest(pc.protocol)
			req.Tools = []schema.ToolDef{{
				Name:        "get_weather",
				Description: "look up weather",
				Parameters:  map[string]any{"type": "object"},
			}}

			resp, err := caller.Call(context.Background(), req)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}

			if resp.FinishReason != FinishReasonToolCalls {
				t.Errorf("FinishReason = %q, want %q", resp.FinishReason, FinishReasonToolCalls)
			}

			calls := resp.Message.ToolCalls()
			if len(calls) != 1 {
				t.Fatalf("len(ToolCalls()) = %d, want 1", len(calls))
			}

			if calls[0].ID != "call-1" || calls[0].Name != "get_weather" {
				t.Errorf("tool call = %+v, want id=call-1 name=get_weather", calls[0])
			}

			assertArgsCity(t, calls[0].Arguments, "SF")
		})
	}
}

// assertArgsCity checks a tool-call argument payload by value, since the two
// vendors do not agree on whitespace or key order in the encoded JSON.
func assertArgsCity(t *testing.T, args, want string) {
	t.Helper()

	var parsed struct {
		City string `json:"city"`
	}

	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("unmarshal arguments %q: %v", args, err)
	}

	if parsed.City != want {
		t.Errorf("arguments city = %q, want %q", parsed.City, want)
	}
}

// TestProviderStream_Text covers a full streaming turn on both providers:
// text arrives in fragments, and the final usage is reported exactly once.
func TestProviderStream_Text(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, sseServer(t, pc.streamBody).URL)

			stream, err := caller.CallStream(context.Background(), simpleRequest(pc.protocol))
			if err != nil {
				t.Fatalf("CallStream: %v", err)
			}
			defer func() { _ = stream.Close() }()

			var acc StreamAccumulator

			for {
				chunk, recvErr := stream.Recv()
				if errors.Is(recvErr, io.EOF) {
					break
				}

				if recvErr != nil {
					t.Fatalf("Recv: %v", recvErr)
				}

				acc.Add(chunk)
			}

			if got := acc.Text(); got != "hello" {
				t.Errorf("accumulated text = %q, want %q", got, "hello")
			}

			if got := acc.FinishReason(); got != FinishReasonStop {
				t.Errorf("FinishReason = %q, want %q", got, FinishReasonStop)
			}

			usage := stream.Usage()
			if usage == nil {
				t.Fatal("stream reported no usage")
			}

			if usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
				t.Errorf("stream usage = %+v, want 10/5/15", usage)
			}
		})
	}
}

// TestProviderStream_ToolCall covers a streamed tool call whose arguments
// arrive split across deltas — the case where a naive decoder loses data.
func TestProviderStream_ToolCall(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, sseServer(t, pc.toolStreamBody).URL)

			stream, err := caller.CallStream(context.Background(), simpleRequest(pc.protocol))
			if err != nil {
				t.Fatalf("CallStream: %v", err)
			}
			defer func() { _ = stream.Close() }()

			var acc StreamAccumulator

			for {
				chunk, recvErr := stream.Recv()
				if errors.Is(recvErr, io.EOF) {
					break
				}

				if recvErr != nil {
					t.Fatalf("Recv: %v", recvErr)
				}

				acc.Add(chunk)
			}

			if got := acc.FinishReason(); got != FinishReasonToolCalls {
				t.Errorf("FinishReason = %q, want %q", got, FinishReasonToolCalls)
			}

			calls := acc.ToolCalls()
			if len(calls) != 1 {
				t.Fatalf("len(ToolCalls()) = %d, want 1", len(calls))
			}

			if calls[0].ID != "call-1" || calls[0].Name != "get_weather" {
				t.Errorf("tool call = %+v, want id=call-1 name=get_weather", calls[0])
			}

			// The fragments must reassemble into valid JSON.
			assertArgsCity(t, calls[0].Arguments, "SF")

			// A streamed turn must replay as a message in the vendor's own
			// wire form, so the next iteration can send it back verbatim.
			msg := acc.AssistantMessage(pc.protocol)
			if msg.Protocol != pc.protocol {
				t.Errorf("assistant message protocol = %q, want %q", msg.Protocol, pc.protocol)
			}

			if got := msg.ToolCalls(); len(got) != 1 || got[0].ID != "call-1" {
				t.Errorf("replayed tool calls = %+v, want the original call", got)
			}
		})
	}
}

// TestProviderCall_ProtocolMismatch covers the dual-track guarantee at the
// call boundary: a message recorded under one protocol must not be sent to a
// caller speaking another, since the stored form is vendor-native.
func TestProviderCall_ProtocolMismatch(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, jsonServer(t, http.StatusOK, pc.textBody).URL)

			other := schema.ProtocolOpenAIChat
			if pc.protocol == schema.ProtocolOpenAIChat {
				other = schema.ProtocolAnthropicMessages
			}

			req := &Request{
				Model:    "test-model",
				Messages: []schema.Message{schema.NewUserMessage(other, "hi")},
			}

			_, err := caller.Call(context.Background(), req)
			if !errors.Is(err, schema.ErrProtocolMismatch) {
				t.Errorf("Call with foreign message = %v, want ErrProtocolMismatch", err)
			}
		})
	}
}

// TestNewCaller_MissingAPIKey covers the construction-time failure both
// callers must report before any network I/O.
func TestNewCaller_MissingAPIKey(t *testing.T) {
	if _, err := NewOpenAIChatCaller("", "http://example.invalid"); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("NewOpenAIChatCaller with no key = %v, want ErrNoAPIKey", err)
	}

	if _, err := NewAnthropicMessagesCaller("", "http://example.invalid"); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("NewAnthropicMessagesCaller with no key = %v, want ErrNoAPIKey", err)
	}
}
