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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
)

func TestLogMiddleware_ChatCompletion_Success(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{
		ID:    "resp-1",
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}}

	wrapped := NewLogMiddleware(WithLogger(logger)).Wrap(mock)

	resp, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "resp-1" {
		t.Fatalf("expected ID 'resp-1', got %q", resp.ID)
	}

	output := buf.String()
	if !strings.Contains(output, "chat_completion_start") {
		t.Fatal("missing chat_completion_start log")
	}

	if !strings.Contains(output, "chat_completion_done") {
		t.Fatal("missing chat_completion_done log")
	}

	if !strings.Contains(output, "gpt-4") {
		t.Fatal("missing model in log")
	}
}

func TestLogMiddleware_ChatCompletion_Error(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mock := &mockCompleter{chatErr: errors.New("api down")}
	wrapped := NewLogMiddleware(WithLogger(logger)).Wrap(mock)

	_, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	output := buf.String()
	if !strings.Contains(output, "chat_completion_error") {
		t.Fatal("missing error log")
	}
}

func TestLogMiddleware_Stream_Success(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mock := &mockCompleter{}
	wrapped := NewLogMiddleware(WithLogger(logger)).Wrap(mock)

	_, _ = wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})

	output := buf.String()
	if !strings.Contains(output, "chat_completion_stream_start") {
		t.Fatal("missing stream start log")
	}
}

func TestLogMiddleware_Stream_Error(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mock := &mockCompleter{streamErr: errors.New("stream fail")}
	wrapped := NewLogMiddleware(WithLogger(logger)).Wrap(mock)

	_, err := wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error")
	}

	output := buf.String()
	if !strings.Contains(output, "chat_completion_stream_error") {
		t.Fatal("missing stream error log")
	}
}

func TestLogMiddleware_DefaultLogger(t *testing.T) {
	m := NewLogMiddleware()
	if m.logger == nil {
		t.Fatal("logger should default to slog.Default()")
	}
}
