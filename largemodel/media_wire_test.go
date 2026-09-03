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

	"github.com/vogo/vage/schema"
)

// These tests pin the Caller-level contract for multimodal input: both the
// sync and streaming build-request paths must encode image/file parts
// identically (they share one buildRequest), and a message a codec cannot
// express must fail before any backend I/O — not mid-request, not silently
// downgraded.

// mediaRequest is a one-turn request whose user message mixes text and an
// image URL, in the given protocol.
func mediaRequest(t *testing.T, proto schema.Protocol) *Request {
	t.Helper()

	img, err := schema.ImageFromURL("https://example.com/cat.png")
	if err != nil {
		t.Fatal(err)
	}

	return &Request{
		Model: "test-model",
		Messages: []schema.Message{
			schema.NewUserMessageWithParts(proto, []schema.MessagePart{
				{Type: schema.MessagePartText, Text: "describe this"},
				img,
			}),
		},
	}
}

// TestProviderCall_MediaEncodesInWireRequest asserts the non-streaming
// build-request path renders the image as each provider's native wire shape.
func TestProviderCall_MediaEncodesInWireRequest(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			url, body := captureWireBody(t, pc.textBody, "application/json")
			caller := pc.newCall(t, url)

			if _, err := caller.Call(context.Background(), mediaRequest(t, pc.protocol)); err != nil {
				t.Fatalf("Call: %v", err)
			}

			assertMediaWireShape(t, pc.protocol, body())
		})
	}
}

// TestProviderStream_Media_MatchesCall covers the "same path" requirement:
// the streaming request build must encode media identically to the
// non-streaming path, for both providers.
func TestProviderStream_Media_MatchesCall(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			url, body := captureWireBody(t, pc.streamBody, "text/event-stream")
			caller := pc.newCall(t, url)

			stream, err := caller.CallStream(context.Background(), mediaRequest(t, pc.protocol))
			if err != nil {
				t.Fatalf("CallStream: %v", err)
			}
			defer func() { _ = stream.Close() }()

			for {
				_, recvErr := stream.Recv()
				if errors.Is(recvErr, io.EOF) {
					break
				}
				if recvErr != nil {
					t.Fatalf("Recv: %v", recvErr)
				}
			}

			assertMediaWireShape(t, pc.protocol, body())
		})
	}
}

// assertMediaWireShape checks the captured request body carries the user
// message's image as the provider's native content shape.
func assertMediaWireShape(t *testing.T, proto schema.Protocol, got map[string]any) {
	t.Helper()

	messages, ok := got["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want a non-empty array", got["messages"])
	}
	userMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want an object", messages[0])
	}
	parts, ok := userMsg["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want a 2-element structured array", userMsg["content"])
	}

	switch proto {
	case schema.ProtocolOpenAIChat:
		imagePart, ok := parts[1].(map[string]any)
		if !ok || imagePart["type"] != "image_url" {
			t.Fatalf("parts[1] = %#v, want type image_url", parts[1])
		}
		imageURL, ok := imagePart["image_url"].(map[string]any)
		if !ok || imageURL["url"] != "https://example.com/cat.png" {
			t.Fatalf("image_url = %#v, want the source URL", imagePart["image_url"])
		}
	case schema.ProtocolAnthropicMessages:
		imagePart, ok := parts[1].(map[string]any)
		if !ok || imagePart["type"] != "image" {
			t.Fatalf("parts[1] = %#v, want type image", parts[1])
		}
		source, ok := imagePart["source"].(map[string]any)
		if !ok || source["type"] != "url" || source["url"] != "https://example.com/cat.png" {
			t.Fatalf("source = %#v, want a url source", imagePart["source"])
		}
	}
}

// TestProviderCall_MediaEncodeErrorSkipsBackend proves an unrepresentable
// combination — an image part on a role media is never valid for — fails
// before any network I/O, for both the sync and the streaming path.
func TestProviderCall_MediaEncodeErrorSkipsBackend(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
			}))
			defer srv.Close()

			caller := pc.newCall(t, srv.URL)
			req := &Request{
				Model: "test-model",
				Messages: []schema.Message{
					schema.NewMessage(pc.protocol, schema.RoleAssistant, []schema.MessagePart{
						{Type: schema.MessagePartImage, URL: "https://example.com/cat.png"},
					}),
				},
			}

			if _, err := caller.Call(context.Background(), req); err == nil {
				t.Fatal("Call: expected an encode error, got nil")
			}
			if _, err := caller.CallStream(context.Background(), req); err == nil {
				t.Fatal("CallStream: expected an encode error, got nil")
			}

			if hits.Load() != 0 {
				t.Fatalf("expected zero network calls, got %d", hits.Load())
			}
		})
	}
}
