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

	"github.com/vogo/vage/schema"
)

func TestModel_New_NoMiddleware(t *testing.T) {
	resp := &Response{
		Message:      schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "hello"),
		FinishReason: FinishReasonStop,
	}
	mock := &mockCompleter{chatResp: resp}
	m := New(mock)

	got, err := m.Call(context.Background(), &Request{})
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
	resp := &Response{
		Message:      schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "ok"),
		FinishReason: FinishReasonStop,
	}
	mock := &mockCompleter{chatResp: resp}

	var mwCalls int
	mw := MiddlewareFunc(func(next Caller) Caller {
		return &CallerFunc{
			Proto: schema.ProtocolOpenAIChat,

			Chat: func(ctx context.Context, req *Request) (*Response, error) {
				mwCalls++
				return next.Call(ctx, req)
			},
			ChatStream: func(ctx context.Context, req *Request) (*Stream, error) {
				return next.CallStream(ctx, req)
			},
		}
	})

	m := New(mock, WithMiddleware(mw))

	got, err := m.Call(context.Background(), &Request{})
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

	_, err := m.CallStream(context.Background(), &Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "stream error" {
		t.Errorf("error = %q, want %q", err.Error(), "stream error")
	}
}

func TestModel_MultipleMiddlewares_Order(t *testing.T) {
	resp := &Response{
		Message:      schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "ok"),
		FinishReason: FinishReasonStop,
	}
	mock := &mockCompleter{chatResp: resp}

	var order []string
	makeMW := func(name string) Middleware {
		return MiddlewareFunc(func(next Caller) Caller {
			return &CallerFunc{
				Proto: schema.ProtocolOpenAIChat,

				Chat: func(ctx context.Context, req *Request) (*Response, error) {
					order = append(order, name)
					return next.Call(ctx, req)
				},
				ChatStream: func(ctx context.Context, req *Request) (*Stream, error) {
					return next.CallStream(ctx, req)
				},
			}
		})
	}

	m := New(mock, WithMiddleware(makeMW("first"), makeMW("second")))
	_, err := m.Call(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("middleware order = %v, want [first second]", order)
	}
}
