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

package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vogo/largemodel/schema"
	"github.com/vogo/vage/tool"
)

func newRegistry() *tool.Registry { return tool.NewRegistry() }

type mapStore struct {
	m map[string]any
}

func newMapStore() *mapStore { return &mapStore{m: make(map[string]any)} }

func (s *mapStore) Get(_ context.Context, key string) (any, error) {
	return s.m[key], nil
}

func (s *mapStore) Set(_ context.Context, key string, value any, _ int64) error {
	s.m[key] = value
	return nil
}

func (s *mapStore) Delete(_ context.Context, key string) error {
	delete(s.m, key)
	return nil
}

func (s *mapStore) List(_ context.Context, prefix string) ([]Entry, error) {
	out := make([]Entry, 0, len(s.m))
	for k, v := range s.m {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, Entry{Key: k, Value: v})
		}
	}
	return out, nil
}

func fixture(t *testing.T) (*tool.Registry, *mapStore) {
	t.Helper()
	store := newMapStore()
	reg := newRegistry()
	if err := Register(reg, store); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg, store
}

func callTool(t *testing.T, reg *tool.Registry, ctx context.Context, name, jsonArgs string) schema.ToolResult {
	t.Helper()
	if _, ok := reg.Get(name); !ok {
		t.Fatalf("tool %q not registered", name)
	}
	res, err := reg.Execute(ctx, name, jsonArgs)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return res
}

func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

func TestRegister_Validates(t *testing.T) {
	if err := Register(nil, newMapStore()); err == nil {
		t.Error("expected nil registry to fail")
	}
	if err := Register(newRegistry(), nil); err == nil {
		t.Error("expected nil store to fail")
	}
}

func TestRegister_AllOrNothing(t *testing.T) {
	reg, _ := fixture(t)
	for _, name := range []string{SetToolName, RecallToolName} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not present after Register", name)
		}
	}
}

func TestMemorySet_HappyPathShared(t *testing.T) {
	reg, store := fixture(t)
	body, _ := json.Marshal(setArgs{Namespace: "project", Key: "style", Value: "use gofumpt"})
	res := callTool(t, reg, context.Background(), SetToolName, string(body))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "scope=shared") {
		t.Errorf("expected shared scope, got %s", resultText(res))
	}
	got, err := store.Get(context.Background(), "project:style")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "use gofumpt" {
		t.Errorf("stored value = %v", got)
	}
}

func TestMemorySet_PrivateNamespace(t *testing.T) {
	reg, _ := fixture(t)
	body, _ := json.Marshal(setArgs{Namespace: "scratch", Key: "draft", Value: "tmp note"})
	res := callTool(t, reg, context.Background(), SetToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "scope=session-private") {
		t.Errorf("expected session-private scope, got %s", resultText(res))
	}
}

func TestMemorySet_EmitsCustomEventWithoutValue(t *testing.T) {
	reg, _ := fixture(t)
	var events []schema.Event
	ctx := schema.WithEmitter(context.Background(), func(e schema.Event) error {
		events = append(events, e)
		return nil
	})
	body, _ := json.Marshal(setArgs{Namespace: "user", Key: "lang", Value: "secret-should-not-log"})
	res := callTool(t, reg, ctx, SetToolName, string(body))
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Type != schema.EventCustom {
		t.Errorf("type = %q, want %q", events[0].Type, schema.EventCustom)
	}
	data, ok := events[0].Data.(schema.CustomEventData)
	if !ok {
		t.Fatalf("data type %T", events[0].Data)
	}
	if data.Name != EventSet {
		t.Errorf("name = %q, want %q", data.Name, EventSet)
	}
	payload, ok := data.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", data.Payload)
	}
	if payload["namespace"] != "user" || payload["key"] != "lang" {
		t.Errorf("payload = %+v", payload)
	}
	if payload["shared"] != true {
		t.Errorf("shared = %v, want true", payload["shared"])
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), "secret-should-not-log") {
		t.Errorf("value leaked into event payload: %s", raw)
	}
}

func TestMemorySet_RejectsEmptyValue(t *testing.T) {
	reg, _ := fixture(t)
	body, _ := json.Marshal(setArgs{Namespace: "project", Key: "x", Value: "  "})
	res := callTool(t, reg, context.Background(), SetToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for empty value")
	}
}

func TestMemorySet_RejectsOversize(t *testing.T) {
	reg, _ := fixture(t)
	body, _ := json.Marshal(setArgs{Namespace: "project", Key: "x", Value: strings.Repeat("a", MaxValueBytes+1)})
	res := callTool(t, reg, context.Background(), SetToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for oversize value")
	}
}

func TestMemorySet_RejectsBadIdent(t *testing.T) {
	reg, _ := fixture(t)
	body, _ := json.Marshal(setArgs{Namespace: "project:evil", Key: "x", Value: "v"})
	res := callTool(t, reg, context.Background(), SetToolName, string(body))
	if !res.IsError {
		t.Fatal("expected error for namespace with colon")
	}
}

type recordingStore struct {
	*mapStore
	lastTTL int64
}

func (s *recordingStore) Set(ctx context.Context, key string, value any, ttl int64) error {
	s.lastTTL = ttl
	return s.mapStore.Set(ctx, key, value, ttl)
}

func TestMemorySet_DeleteAndTTL(t *testing.T) {
	store := &recordingStore{mapStore: newMapStore()}
	reg := newRegistry()
	if err := Register(reg, store); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx := context.Background()

	setRes := callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "project", Key: "style", Value: "gofumpt", TTL: 30}))
	if setRes.IsError {
		t.Fatalf("set: %s", resultText(setRes))
	}
	if store.lastTTL != 30 {
		t.Fatalf("ttl passed to Set = %d, want 30", store.lastTTL)
	}

	recall := callTool(t, reg, ctx, RecallToolName, `{"namespace":"project","prefix":"style"}`)
	if recall.IsError || !strings.Contains(resultText(recall), "style") {
		t.Fatalf("recall before delete: %s", resultText(recall))
	}

	del := callTool(t, reg, ctx, SetToolName, `{"op":"delete","namespace":"project","key":"style"}`)
	if del.IsError {
		t.Fatalf("delete: %s", resultText(del))
	}
	recall = callTool(t, reg, ctx, RecallToolName, `{"namespace":"project","prefix":"style"}`)
	if recall.IsError {
		t.Fatalf("recall: %s", resultText(recall))
	}
	if strings.Contains(resultText(recall), "style") && !strings.Contains(resultText(recall), "no entries") {
		t.Fatalf("key still recalled after delete: %s", resultText(recall))
	}

	missing := callTool(t, reg, ctx, SetToolName, `{"op":"delete","namespace":"project","key":"missing"}`)
	if missing.IsError {
		t.Fatalf("delete missing key: %s", resultText(missing))
	}

	withValue := callTool(t, reg, ctx, SetToolName, `{"op":"delete","namespace":"project","key":"style","value":"nope"}`)
	if !withValue.IsError {
		t.Fatal("delete with value must fail")
	}
	neg := callTool(t, reg, ctx, SetToolName, `{"namespace":"project","key":"style","value":"x","ttl":-1}`)
	if !neg.IsError {
		t.Fatal("negative ttl must fail")
	}
	unknown := callTool(t, reg, ctx, SetToolName, `{"op":"clear","namespace":"project","key":"style"}`)
	if !unknown.IsError {
		t.Fatal("unknown op must fail")
	}
}

func TestMemorySet_DeleteEmitsEventWithoutValue(t *testing.T) {
	reg, _ := fixture(t)
	var events []schema.Event
	ctx := schema.WithEmitter(context.Background(), func(e schema.Event) error {
		events = append(events, e)
		return nil
	})
	_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "notes", Key: "old", Value: "secret-should-not-log"}))
	events = nil
	res := callTool(t, reg, ctx, SetToolName, `{"op":"delete","namespace":"notes","key":"old"}`)
	if res.IsError {
		t.Fatalf("delete: %s", resultText(res))
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	data, ok := events[0].Data.(schema.CustomEventData)
	if !ok {
		t.Fatalf("data type %T", events[0].Data)
	}
	if data.Name != EventDelete {
		t.Errorf("name = %q, want %q", data.Name, EventDelete)
	}
	raw, _ := json.Marshal(data.Payload)
	if strings.Contains(string(raw), "secret-should-not-log") {
		t.Errorf("value leaked into delete event: %s", raw)
	}
}

func TestMemorySet_InvalidJSON(t *testing.T) {
	reg, _ := fixture(t)
	res := callTool(t, reg, context.Background(), SetToolName, "{ not json")
	if !res.IsError {
		t.Fatal("expected error for malformed json")
	}
}

func TestMemoryRecall_EmptyIsSuccess(t *testing.T) {
	reg, _ := fixture(t)
	res := callTool(t, reg, context.Background(), RecallToolName, `{}`)
	if res.IsError {
		t.Fatalf("empty recall must not be an error: %s", resultText(res))
	}
	if resultText(res) != "no entries" {
		t.Errorf("got %q", resultText(res))
	}
}

func TestMemoryRecall_FiltersSharedWhenNamespaceEmpty(t *testing.T) {
	reg, _ := fixture(t)
	ctx := context.Background()
	_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "project", Key: "a", Value: "shared-a"}))
	_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "scratch", Key: "b", Value: "private-b"}))

	res := callTool(t, reg, ctx, RecallToolName, `{}`)
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	out := resultText(res)
	if !strings.Contains(out, "project:a") {
		t.Errorf("shared entry missing: %s", out)
	}
	if strings.Contains(out, "scratch") || strings.Contains(out, "private-b") {
		t.Errorf("private entry leaked into empty-namespace recall: %s", out)
	}
}

func TestMemoryRecall_ExplicitPrivateNamespace(t *testing.T) {
	reg, _ := fixture(t)
	ctx := context.Background()
	_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "scratch", Key: "b", Value: "private-b"}))
	res := callTool(t, reg, ctx, RecallToolName, `{"namespace":"scratch"}`)
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "scratch:b") {
		t.Errorf("got %s", resultText(res))
	}
}

func TestMemoryRecall_PrefixAndLimit(t *testing.T) {
	reg, _ := fixture(t)
	ctx := context.Background()
	for _, k := range []string{"alpha", "alpine", "beta"} {
		_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "notes", Key: k, Value: "v-" + k}))
	}
	res := callTool(t, reg, ctx, RecallToolName, `{"namespace":"notes","prefix":"alp","limit":1}`)
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	out := resultText(res)
	if !strings.HasPrefix(out, "1 entries:") {
		t.Errorf("expected 1 entry, got %s", out)
	}
	if strings.Contains(out, "beta") {
		t.Errorf("prefix filter leaked beta: %s", out)
	}
}

func TestMemoryRecall_LimitClamp(t *testing.T) {
	reg, _ := fixture(t)
	ctx := context.Background()
	for i := range 60 {
		key := identForIndex(i)
		_ = callTool(t, reg, ctx, SetToolName, mustJSON(setArgs{Namespace: "notes", Key: key, Value: "v"}))
	}
	res := callTool(t, reg, ctx, RecallToolName, `{"namespace":"notes","limit":999}`)
	if res.IsError {
		t.Fatalf("error: %s", resultText(res))
	}
	if !strings.HasPrefix(resultText(res), "50 entries:") {
		t.Errorf("expected clamp to %d, got %s", MaxLimit, resultText(res))
	}
}

func TestIsSharedNamespace_MEMR2(t *testing.T) {
	for _, ns := range SharedNamespaceNames {
		if !IsSharedNamespace(ns) {
			t.Errorf("%q should be shared", ns)
		}
	}
	if IsSharedNamespace("scratch") {
		t.Error("scratch must not be shared")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func identForIndex(i int) string {
	return "k" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [4]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
