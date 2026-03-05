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
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/schema"
)

func TestMetricsMiddleware_ChatCompletion(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{
		ID:    "ok",
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}

	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{
		Model:    "gpt-4",
		Messages: []aimodel.Message{{Role: aimodel.RoleUser}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	startData, ok := events[0].Data.(schema.LLMCallStartData)
	if !ok {
		t.Fatalf("events[0].Data type = %T, want LLMCallStartData", events[0].Data)
	}

	if startData.Model != "gpt-4" {
		t.Errorf("startData.Model = %q, want %q", startData.Model, "gpt-4")
	}

	if startData.Messages != 1 {
		t.Errorf("startData.Messages = %d, want 1", startData.Messages)
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if endData.TotalTokens != 15 {
		t.Errorf("endData.TotalTokens = %d, want 15", endData.TotalTokens)
	}
}

func TestMetricsMiddleware_ChatCompletion_Error(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{chatErr: errors.New("API error")}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[1].Type != schema.EventLLMCallError {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallError)
	}

	errData, ok := events[1].Data.(schema.LLMCallErrorData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallErrorData", events[1].Data)
	}

	if errData.Error != "API error" {
		t.Errorf("errData.Error = %q, want %q", errData.Error, "API error")
	}
}

func TestMetricsMiddleware_Stream_Error(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{streamErr: errors.New("stream error")}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	startData, ok := events[0].Data.(schema.LLMCallStartData)
	if !ok {
		t.Fatalf("events[0].Data type = %T, want LLMCallStartData", events[0].Data)
	}

	if !startData.Stream {
		t.Error("startData.Stream should be true")
	}

	if events[1].Type != schema.EventLLMCallError {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallError)
	}

	errData, ok := events[1].Data.(schema.LLMCallErrorData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallErrorData", events[1].Data)
	}

	if !errData.Stream {
		t.Error("errData.Stream should be true")
	}
}

func TestMetricsMiddleware_Stream_Success(t *testing.T) {
	var events []schema.Event
	dispatch := func(_ context.Context, e schema.Event) {
		events = append(events, e)
	}

	mock := &mockCompleter{}
	mw := NewMetricsMiddleware(dispatch)
	wrapped := mw.Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Type != schema.EventLLMCallStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, schema.EventLLMCallStart)
	}

	if events[1].Type != schema.EventLLMCallEnd {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, schema.EventLLMCallEnd)
	}

	endData, ok := events[1].Data.(schema.LLMCallEndData)
	if !ok {
		t.Fatalf("events[1].Data type = %T, want LLMCallEndData", events[1].Data)
	}

	if !endData.Stream {
		t.Error("endData.Stream should be true")
	}
}

func TestMetricsMiddleware_NilDispatch(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil dispatch")
		}
	}()

	NewMetricsMiddleware(nil)
}
