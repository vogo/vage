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
)

func TestModel_New_NoMiddleware(t *testing.T) {
	resp := &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("hello")},
			FinishReason: aimodel.FinishReasonStop,
		}},
	}
	mock := &mockCompleter{chatResp: resp}
	m := New(mock)

	got, err := m.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != resp {
		t.Errorf("response mismatch: got %v, want %v", got, resp)
	}
	if mock.chatCalls != 1 {
		t.Errorf("chatCalls = %d, want 1", mock.chatCalls)
	}
}

func TestModel_New_WithMiddleware(t *testing.T) {
	resp := &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("ok")},
			FinishReason: aimodel.FinishReasonStop,
		}},
	}
	mock := &mockCompleter{chatResp: resp}

	var mwCalls int
	mw := MiddlewareFunc(func(next aimodel.ChatCompleter) aimodel.ChatCompleter {
		return &completerFunc{
			chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
				mwCalls++
				return next.ChatCompletion(ctx, req)
			},
			stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
				return next.ChatCompletionStream(ctx, req)
			},
		}
	})

	m := New(mock, WithMiddleware(mw))

	got, err := m.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != resp {
		t.Errorf("response mismatch")
	}
	if mwCalls != 1 {
		t.Errorf("middleware calls = %d, want 1", mwCalls)
	}
	if mock.chatCalls != 1 {
		t.Errorf("chatCalls = %d, want 1", mock.chatCalls)
	}
}

func TestModel_ChatCompletionStream(t *testing.T) {
	mock := &mockCompleter{streamErr: errors.New("stream error")}
	m := New(mock)

	_, err := m.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "stream error" {
		t.Errorf("error = %q, want %q", err.Error(), "stream error")
	}
}

func TestModel_MultipleMiddlewares_Order(t *testing.T) {
	resp := &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("ok")},
			FinishReason: aimodel.FinishReasonStop,
		}},
	}
	mock := &mockCompleter{chatResp: resp}

	var order []string
	makeMW := func(name string) Middleware {
		return MiddlewareFunc(func(next aimodel.ChatCompleter) aimodel.ChatCompleter {
			return &completerFunc{
				chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
					order = append(order, name)
					return next.ChatCompletion(ctx, req)
				},
				stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
					return next.ChatCompletionStream(ctx, req)
				},
			}
		})
	}

	m := New(mock, WithMiddleware(makeMW("first"), makeMW("second")))
	_, err := m.ChatCompletion(context.Background(), &aimodel.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("middleware order = %v, want [first second]", order)
	}
}
