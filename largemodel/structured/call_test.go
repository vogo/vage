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

package structured_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/largemodel/structured"
	"github.com/vogo/vage/schema"
)

type answer struct {
	Value int `json:"value"`
}

func nativeCaller(responses ...string) *largemodel.FakeCaller {
	out := make([]*largemodel.Response, len(responses))
	for i, text := range responses {
		out[i] = largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, text, schema.Usage{})
	}

	return &largemodel.FakeCaller{
		Responses:   out,
		DeclaredSet: true,
		Declared:    largemodel.Capabilities{StructuredOutput: largemodel.SupportNative},
	}
}

func TestCall_FromRequestSchemaAndDoesNotMutate(t *testing.T) {
	fake := nativeCaller(`{"value": 3}`)
	req := &largemodel.Request{
		Messages:       []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "n")},
		ResponseSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}},
	}
	got, err := structured.Call[answer](context.Background(), fake, req)
	if err != nil {
		t.Fatal(err)
	}

	if got.Value.Value != 3 || got.Response == nil {
		t.Fatalf("got %+v", got)
	}

	if req.ResponseSchema == nil {
		t.Fatal("original request mutated")
	}

	if len(req.Messages) != 1 {
		t.Fatalf("original messages mutated: %d", len(req.Messages))
	}
}

func TestCall_DerivesSchemaFromT(t *testing.T) {
	fake := nativeCaller(`{"value": 9}`)
	req := &largemodel.Request{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "n")}}
	got, err := structured.Call[answer](context.Background(), fake, req)
	if err != nil {
		t.Fatal(err)
	}

	if got.Value.Value != 9 {
		t.Fatalf("got %+v", got.Value)
	}

	if req.ResponseSchema != nil {
		t.Fatal("original request must not gain a schema")
	}

	if fake.Requests()[0].ResponseSchema == nil {
		t.Fatal("clone must carry a derived schema")
	}
}

func TestCall_NativeUnknownFailsClosed(t *testing.T) {
	fake := &largemodel.FakeCaller{
		Responses: []*largemodel.Response{largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, `{"value":1}`, schema.Usage{})},
	}
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"type": "object"},
	})
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageCapability || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestCall_PromptFallbackWithPromptCapability(t *testing.T) {
	fake := &largemodel.FakeCaller{
		Responses:   []*largemodel.Response{largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, `{"value":2}`, schema.Usage{})},
		DeclaredSet: true,
		Declared:    largemodel.Capabilities{StructuredOutput: largemodel.SupportPrompt},
	}
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"type": "object"},
	})
	if err == nil {
		t.Fatal("default native requirement must reject prompt-only")
	}

	got, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"type": "object"},
	}, structured.AllowPromptFallback())
	if err != nil {
		t.Fatal(err)
	}

	if got.Value.Value != 2 {
		t.Fatalf("got %+v", got.Value)
	}
}

func TestCall_JSONDecodeAndRepairBudget(t *testing.T) {
	fake := nativeCaller("not-json", `{"value": 4}`)
	req := &largemodel.Request{
		Messages:       []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "n")},
		ResponseSchema: map[string]any{"type": "object"},
	}
	got, err := structured.Call[answer](context.Background(), fake, req, structured.WithRepairAttempts(1))
	if err != nil {
		t.Fatal(err)
	}

	if got.Value.Value != 4 || fake.Calls() != 2 {
		t.Fatalf("value=%v calls=%d", got.Value, fake.Calls())
	}

	if len(req.Messages) != 1 {
		t.Fatal("original request mutated by repair")
	}

	if len(fake.Requests()[1].Messages) != 2 {
		t.Fatalf("repair must append a diagnostic, got %d messages", len(fake.Requests()[1].Messages))
	}
}

func TestCall_RepairExhaustedKeepsRaw(t *testing.T) {
	fake := nativeCaller("not-json")
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"type": "object"},
	}, structured.WithRepairAttempts(0))
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageDecode || se.RawText != "not-json" || se.Response == nil {
		t.Fatalf("err=%+v", se)
	}

	if fake.Calls() != 1 {
		t.Fatalf("calls = %d", fake.Calls())
	}
}

func TestCall_TransportErrorDoesNotRepair(t *testing.T) {
	transport := errors.New("connection reset")
	fake := &largemodel.FakeCaller{
		Err:         transport,
		DeclaredSet: true,
		Declared:    largemodel.Capabilities{StructuredOutput: largemodel.SupportNative},
	}
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"type": "object"},
	}, structured.WithRepairAttempts(3))
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageTransport || !errors.Is(err, transport) {
		t.Fatalf("err=%v", err)
	}

	if fake.Calls() != 0 {
		// FakeCaller.Call still records the request before returning Err, and
		// increments calls only on a scripted response. Err path does not increment.
		t.Fatalf("scripted calls = %d", fake.Calls())
	}
}

func TestCall_SchemaValidationAndTypedValidator(t *testing.T) {
	schemaObj := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"value": map[string]any{"type": "integer", "minimum": 10}},
		"required":             []string{"value"},
		"additionalProperties": false,
	}
	fake := nativeCaller(`{"value": 1}`, `{"value": 11}`)
	got, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{ResponseSchema: schemaObj},
		structured.WithValidation(), structured.WithRepairAttempts(1))
	if err != nil {
		t.Fatal(err)
	}

	if got.Value.Value != 11 || fake.Calls() != 2 {
		t.Fatalf("value=%v calls=%d", got.Value, fake.Calls())
	}

	fake = nativeCaller(`{"value": 11}`)
	_, err = structured.Call[answer](context.Background(), fake, &largemodel.Request{ResponseSchema: schemaObj},
		structured.WithValidator(func(a answer) error {
			if a.Value%2 == 0 {
				return nil
			}

			return errors.New("must be even")
		}))
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageValidate {
		t.Fatalf("err=%v", err)
	}
}

func TestCall_NegativeRepairIsConfigError(t *testing.T) {
	fake := nativeCaller(`{"value":1}`)
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{}, structured.WithRepairAttempts(-1))
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageConfig || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestCall_CancelKeepsLastResponse(t *testing.T) {
	fake := nativeCaller("not-json", `{"value":1}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := structured.Call[answer](ctx, fake, &largemodel.Request{ResponseSchema: map[string]any{"type": "object"}},
		structured.WithRepairAttempts(1))
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageTransport || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}

func TestCall_RemoteRefFailsBeforeCall(t *testing.T) {
	fake := nativeCaller(`{"value":1}`)
	_, err := structured.Call[answer](context.Background(), fake, &largemodel.Request{
		ResponseSchema: map[string]any{"$ref": "https://example.com/schema.json"},
	})
	var se *structured.Error
	if !errors.As(err, &se) || se.Stage != structured.StageConfig || fake.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, fake.Calls())
	}
}
