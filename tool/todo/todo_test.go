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

package todo

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

func TestHandler_RejectsMissingSessionID(t *testing.T) {
	tt := New(NewStore())
	result, err := tt.Handler()(context.Background(), ToolName, `{"todos":[]}`)
	if err != nil {
		t.Fatalf("handler must not return Go error; got %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true, got result=%+v", result)
	}
	if !strings.Contains(result.Content[0].Text, "session id missing") {
		t.Fatalf("expected 'session id missing' text; got %q", result.Content[0].Text)
	}
}

func TestHandler_RejectsInvalidJSON(t *testing.T) {
	tt := New(NewStore())
	ctx := schema.WithSessionID(context.Background(), "sess")
	result, err := tt.Handler()(ctx, ToolName, `not json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true")
	}
	if !strings.Contains(result.Content[0].Text, "invalid arguments") {
		t.Fatalf("unexpected error text: %q", result.Content[0].Text)
	}
}

func TestHandler_Success_EmitsEventAndReturnsConfirmation(t *testing.T) {
	tt := New(NewStore())

	var captured []schema.Event
	em := schema.Emitter(func(e schema.Event) error {
		captured = append(captured, e)
		return nil
	})
	ctx := schema.WithSessionID(context.Background(), "sess")
	ctx = schema.WithEmitter(ctx, em)

	args := `{"todos":[
		{"content":"Read code","active_form":"Reading code","status":"pending"},
		{"content":"Edit code","active_form":"Editing code","status":"in_progress"}
	]}`
	result, err := tt.Handler()(ctx, ToolName, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %+v", result)
	}

	confirmRe := regexp.MustCompile(`^ok \(v\d+, \d+ items\)$`)
	text := result.Content[0].Text
	if !confirmRe.MatchString(text) {
		t.Fatalf("confirmation text does not match %q: %q", confirmRe.String(), text)
	}

	if len(captured) != 1 {
		t.Fatalf("expected exactly 1 emitted event, got %d", len(captured))
	}
	ev := captured[0]
	if ev.Type != schema.EventTodoUpdate {
		t.Fatalf("expected %q event, got %q", schema.EventTodoUpdate, ev.Type)
	}
	if ev.SessionID != "sess" {
		t.Fatalf("expected sessionID 'sess' on event, got %q", ev.SessionID)
	}
	data, ok := ev.Data.(schema.TodoUpdateData)
	if !ok {
		t.Fatalf("expected TodoUpdateData payload, got %T", ev.Data)
	}
	if data.Version != 1 {
		t.Fatalf("expected version 1, got %d", data.Version)
	}
	if len(data.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(data.Items))
	}
	if data.Items[0].Status != "pending" || data.Items[1].Status != "in_progress" {
		t.Fatalf("unexpected statuses: %+v", data.Items)
	}
	if data.Items[0].ID == "" || data.Items[1].ID == "" {
		t.Fatalf("expected ids to be assigned: %+v", data.Items)
	}
}

func TestHandler_NilEmitter_NoPanic(t *testing.T) {
	tt := New(NewStore())
	ctx := schema.WithSessionID(context.Background(), "sess")
	// No Emitter attached.
	if _, err := tt.Handler()(ctx, ToolName, `{"todos":[{"content":"A","active_form":"Doing A","status":"pending"}]}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandler_StoreApplyError_SurfacesAsErrorResult(t *testing.T) {
	tt := New(NewStore())
	ctx := schema.WithSessionID(context.Background(), "sess")
	args := `{"todos":[
		{"content":"A","active_form":"Doing A","status":"in_progress"},
		{"content":"B","active_form":"Doing B","status":"in_progress"}
	]}`
	result, err := tt.Handler()(ctx, ToolName, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError for two in_progress, got %+v", result)
	}
	if !strings.Contains(result.Content[0].Text, "only one in_progress") {
		t.Fatalf("unexpected error text: %q", result.Content[0].Text)
	}
}

func TestRegister_NilStore(t *testing.T) {
	reg := tool.NewRegistry()
	if err := Register(reg, nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestRegister_AddsToolAndDuplicateIsRejected(t *testing.T) {
	reg := tool.NewRegistry()
	store := NewStore()
	if err := Register(reg, store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := reg.Get(ToolName); !ok {
		t.Fatalf("%q not found in registry", ToolName)
	}
	if err := Register(reg, store); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestToolDef_IsReadOnly(t *testing.T) {
	tt := New(NewStore())
	def := tt.ToolDef()
	if !def.ReadOnly {
		t.Fatal("todo_write must be marked ReadOnly (no workspace side effect)")
	}
	if def.Name != ToolName {
		t.Fatalf("unexpected tool name: %q", def.Name)
	}
}
