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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// The failure paths matter as much as the happy ones: retry, circuit breaking
// and budget accounting all key off the normalized error, so each provider's
// very different error body has to classify identically.

// errorBodies pairs each protocol with a vendor-shaped error payload.
type errorBody struct {
	name     string
	protocol schema.Protocol
	newCall  callerFactory
	body     string
}

func errorBodies() []errorBody {
	return []errorBody{
		{
			name:     "openai",
			protocol: schema.ProtocolOpenAIChat,
			newCall:  newOpenAICaller,
			body:     `{"error":{"message":"boom","type":"invalid_request_error","code":"bad_thing"}}`,
		},
		{
			name:     "anthropic",
			protocol: schema.ProtocolAnthropicMessages,
			newCall:  newAnthropicCaller,
			body:     `{"type":"error","error":{"type":"invalid_request_error","message":"boom"}}`,
		},
	}
}

// TestProviderError_StatusCodes covers the non-2xx classification both
// callers must produce, across the retryable and non-retryable ranges.
func TestProviderError_StatusCodes(t *testing.T) {
	statuses := []int{
		http.StatusBadRequest,          // not retryable
		http.StatusTooManyRequests,     // retryable
		http.StatusInternalServerError, // retryable
		http.StatusServiceUnavailable,  // retryable
	}

	for _, eb := range errorBodies() {
		for _, status := range statuses {
			t.Run(eb.name+"/"+http.StatusText(status), func(t *testing.T) {
				caller := eb.newCall(t, jsonServer(t, status, eb.body).URL)

				_, err := caller.Call(context.Background(), simpleRequest(eb.protocol))
				if err == nil {
					t.Fatal("expected an error")
				}

				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("error = %v, want an *APIError", err)
				}

				if apiErr.StatusCode != status {
					t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, status)
				}

				if apiErr.Message != "boom" {
					t.Errorf("Message = %q, want %q", apiErr.Message, "boom")
				}

				// Retry decisions are made on the normalized error, so the
				// two providers must agree on which failures are transient.
				wantRetry := status != http.StatusBadRequest
				if got := isRetryable(err); got != wantRetry {
					t.Errorf("isRetryable = %v, want %v", got, wantRetry)
				}
			})
		}
	}
}

// TestProviderError_StreamStatus covers a stream that fails before any event
// is delivered: the error must surface from CallStream itself.
func TestProviderError_StreamStatus(t *testing.T) {
	for _, eb := range errorBodies() {
		t.Run(eb.name, func(t *testing.T) {
			caller := eb.newCall(t, jsonServer(t, http.StatusTooManyRequests, eb.body).URL)

			stream, err := caller.CallStream(context.Background(), simpleRequest(eb.protocol))
			if err == nil {
				_ = stream.Close()
				t.Fatal("expected an error establishing the stream")
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an *APIError", err)
			}

			if apiErr.StatusCode != http.StatusTooManyRequests {
				t.Errorf("StatusCode = %d, want 429", apiErr.StatusCode)
			}
		})
	}
}

// TestProviderError_MalformedResponse covers a 2xx reply whose body is not
// valid vendor JSON — the caller must fail rather than yield a blank turn.
func TestProviderError_MalformedResponse(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, jsonServer(t, http.StatusOK, `{"not":`).URL)

			if _, err := caller.Call(context.Background(), simpleRequest(pc.protocol)); err == nil {
				t.Fatal("expected an error decoding a malformed response")
			}
		})
	}
}

// TestProviderError_StreamInterrupted covers a stream cut off mid-flight. The
// consumer must observe the truncation rather than silently treating the
// partial turn as complete.
func TestProviderError_StreamInterrupted(t *testing.T) {
	// A tool call whose arguments never finish arriving: the stream ends
	// without the terminal event that reports the finish reason.
	bodies := map[schema.Protocol]string{
		schema.ProtocolOpenAIChat: sse(
			"data: " + `{"id":"1","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}` + "\n\n",
		),
		schema.ProtocolAnthropicMessages: sse(
			"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n",
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`+"\n\n",
		),
	}

	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, sseServer(t, bodies[pc.protocol]).URL)

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

			if got := acc.Text(); got != "par" {
				t.Errorf("accumulated text = %q, want the partial %q", got, "par")
			}

			// No terminal event arrived, so the turn must not claim to have
			// stopped normally — that is how callers detect truncation.
			if got := acc.FinishReason(); got != "" {
				t.Errorf("FinishReason = %q, want empty on an interrupted stream", got)
			}
		})
	}
}

// TestProviderStream_CloseIsIdempotent covers early termination: a consumer
// that abandons a stream must release it exactly once, no matter how often
// Close is called.
func TestProviderStream_CloseIsIdempotent(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, sseServer(t, pc.streamBody).URL)

			stream, err := caller.CallStream(context.Background(), simpleRequest(pc.protocol))
			if err != nil {
				t.Fatalf("CallStream: %v", err)
			}

			// Read one chunk, then abandon the stream.
			if _, err := stream.Recv(); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("Recv: %v", err)
			}

			for i := range 3 {
				if err := stream.Close(); err != nil {
					t.Errorf("Close #%d = %v, want nil", i+1, err)
				}
			}

			// Reading a closed stream is an error, not a panic or a hang.
			if _, err := stream.Recv(); !errors.Is(err, ErrStreamClosed) {
				t.Errorf("Recv after Close = %v, want ErrStreamClosed", err)
			}
		})
	}
}

// TestProviderStream_CloseFiresUsageOnce covers the accounting guarantee that
// keeps budgets honest: the close callback runs exactly once even when a
// consumer closes repeatedly.
func TestProviderStream_CloseFiresUsageOnce(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			caller := pc.newCall(t, sseServer(t, pc.streamBody).URL)

			stream, err := caller.CallStream(context.Background(), simpleRequest(pc.protocol))
			if err != nil {
				t.Fatalf("CallStream: %v", err)
			}

			var fired atomic.Int32

			stream = WrapStreamClose(stream, func(*schema.Usage) { fired.Add(1) })

			for {
				_, recvErr := stream.Recv()
				if recvErr != nil {
					break
				}
			}

			_ = stream.Close()
			_ = stream.Close()

			if got := fired.Load(); got != 1 {
				t.Errorf("close callback fired %d times, want exactly 1", got)
			}
		})
	}
}

// TestProviderCall_ContextCancelled covers cancellation: an in-flight call
// must abort rather than block on a server that never answers.
func TestProviderCall_ContextCancelled(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			// A server that hangs until the client gives up.
			released := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-released:
				}
			}))

			t.Cleanup(func() {
				close(released)
				srv.Close()
			})

			caller := pc.newCall(t, srv.URL)

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			_, err := caller.Call(ctx, simpleRequest(pc.protocol))
			if err == nil {
				t.Fatal("expected an error from the cancelled call")
			}

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
			}
		})
	}
}
