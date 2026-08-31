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

// These tests pin the native ResponseSchema wire mapping: what OpenAI Chat
// and Anthropic Messages actually put on the wire, with and without the
// field set, for both the non-streaming and the streaming path.

// captureWireBody starts a server that records the request body verbatim and
// answers every request with respBody, and returns the server URL plus a
// decoder for whatever was last captured.
// responseSchemaFormatWireName is the json_schema name vage must put on the
// wire. It is spelled out here rather than read from the codec so the test
// pins the observable request shape, not whatever constant produced it: the
// name is part of the prompt-cache key every identical request depends on.
const responseSchemaFormatWireName = "vage_response_schema"

func captureWireBody(t *testing.T, respBody, contentType string) (string, func() map[string]any) {
	t.Helper()

	var raw []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		if raw, err = io.ReadAll(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}

		w.Header().Set("Content-Type", contentType)

		if _, err := io.WriteString(w, respBody); err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return srv.URL, func() map[string]any {
		t.Helper()

		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode captured request body: %v", err)
		}

		return m
	}
}

// assertSchemaEqual compares two schema values by their canonical JSON
// encoding, since a value round-tripped through the wire (map[string]any,
// []any, float64 numbers) is not == to the caller's original Go literal.
func assertSchemaEqual(t *testing.T, got, want any) {
	t.Helper()

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got schema: %v", err)
	}

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want schema: %v", err)
	}

	if string(gotJSON) != string(wantJSON) {
		t.Errorf("schema = %s, want %s", gotJSON, wantJSON)
	}
}

func testResponseSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"required":   []string{"answer"},
	}
}

// TestProviderCall_ResponseSchemaUnset_NoNativeField covers the "unset"
// branch: neither provider emits any structured-output field when
// ResponseSchema is nil, matching today's wire exactly.
func TestProviderCall_ResponseSchemaUnset_NoNativeField(t *testing.T) {
	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			url, body := captureWireBody(t, pc.textBody, "application/json")
			caller := pc.newCall(t, url)

			if _, err := caller.Call(context.Background(), simpleRequest(pc.protocol)); err != nil {
				t.Fatalf("Call: %v", err)
			}

			got := body()
			if _, ok := got["response_format"]; ok {
				t.Errorf("response_format present without ResponseSchema: %#v", got["response_format"])
			}

			if _, ok := got["output_config"]; ok {
				t.Errorf("output_config present without ResponseSchema: %#v", got["output_config"])
			}
		})
	}
}

// TestProviderCall_ResponseSchemaSet_NativeField covers the native-mapping
// branch on both providers, combined with Tools to prove the two constraints
// are encoded independently and neither overrides the other.
func TestProviderCall_ResponseSchemaSet_NativeField(t *testing.T) {
	respSchema := testResponseSchema()

	t.Run("openai", func(t *testing.T) {
		pc := providerCases()[0]
		url, body := captureWireBody(t, pc.textBody, "application/json")
		caller := pc.newCall(t, url)

		req := simpleRequest(pc.protocol)
		req.ResponseSchema = respSchema
		req.Tools = []schema.ToolDef{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}}

		if _, err := caller.Call(context.Background(), req); err != nil {
			t.Fatalf("Call: %v", err)
		}

		got := body()

		if _, ok := got["tools"]; !ok {
			t.Error("tools missing when ResponseSchema is also set")
		}

		rf, ok := got["response_format"].(map[string]any)
		if !ok {
			t.Fatalf("response_format = %#v, want a json_schema object", got["response_format"])
		}

		if rf["type"] != "json_schema" {
			t.Errorf("response_format.type = %v, want json_schema", rf["type"])
		}

		js, ok := rf["json_schema"].(map[string]any)
		if !ok {
			t.Fatalf("response_format.json_schema = %#v", rf["json_schema"])
		}

		if js["name"] != responseSchemaFormatWireName {
			t.Errorf("json_schema.name = %v, want %q", js["name"], responseSchemaFormatWireName)
		}

		if strict, _ := js["strict"].(bool); !strict {
			t.Errorf("json_schema.strict = %v, want true", js["strict"])
		}

		assertSchemaEqual(t, js["schema"], respSchema)
	})

	t.Run("anthropic", func(t *testing.T) {
		pc := providerCases()[1]
		url, body := captureWireBody(t, pc.textBody, "application/json")
		caller := pc.newCall(t, url)

		req := simpleRequest(pc.protocol)
		req.ResponseSchema = respSchema
		req.Tools = []schema.ToolDef{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}}

		if _, err := caller.Call(context.Background(), req); err != nil {
			t.Fatalf("Call: %v", err)
		}

		got := body()

		if _, ok := got["tools"]; !ok {
			t.Error("tools missing when ResponseSchema is also set")
		}

		oc, ok := got["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("output_config = %#v, want an object", got["output_config"])
		}

		format, ok := oc["format"].(map[string]any)
		if !ok {
			t.Fatalf("output_config.format = %#v", oc["format"])
		}

		if format["type"] != "json_schema" {
			t.Errorf("output_config.format.type = %v, want json_schema", format["type"])
		}

		assertSchemaEqual(t, format["schema"], respSchema)
	})
}

// TestProviderStream_ResponseSchema_MatchesCall covers the "same path"
// requirement: the streaming request build must set the identical
// constraint field as the non-streaming path, for both providers.
func TestProviderStream_ResponseSchema_MatchesCall(t *testing.T) {
	respSchema := testResponseSchema()

	for _, pc := range providerCases() {
		t.Run(pc.name, func(t *testing.T) {
			url, body := captureWireBody(t, pc.streamBody, "text/event-stream")
			caller := pc.newCall(t, url)

			req := simpleRequest(pc.protocol)
			req.ResponseSchema = respSchema

			stream, err := caller.CallStream(context.Background(), req)
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

			got := body()

			switch pc.protocol {
			case schema.ProtocolOpenAIChat:
				rf, ok := got["response_format"].(map[string]any)
				if !ok {
					t.Fatalf("response_format = %#v", got["response_format"])
				}

				js, ok := rf["json_schema"].(map[string]any)
				if !ok {
					t.Fatalf("json_schema = %#v", rf["json_schema"])
				}

				assertSchemaEqual(t, js["schema"], respSchema)
			case schema.ProtocolAnthropicMessages:
				oc, ok := got["output_config"].(map[string]any)
				if !ok {
					t.Fatalf("output_config = %#v", got["output_config"])
				}

				format, ok := oc["format"].(map[string]any)
				if !ok {
					t.Fatalf("output_config.format = %#v", oc["format"])
				}

				assertSchemaEqual(t, format["schema"], respSchema)
			}
		})
	}
}
