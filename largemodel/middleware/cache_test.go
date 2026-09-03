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

package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/vogo/vage/largemodel"

	"github.com/vogo/vage/schema"
)

func TestCacheMiddleware_HitAndMiss(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{chatResp: &largemodel.Response{ID: "fresh"}}

	wrapped := NewCacheMiddleware(cache, WithCacheTTL(time.Minute)).Wrap(mock)
	ctx := context.Background()
	req := &largemodel.Request{Model: "gpt-4", Messages: []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello"),
	}}

	// Miss: should call through.
	resp, err := wrapped.Call(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "fresh" {
		t.Fatalf("expected ID 'fresh', got %q", resp.ID)
	}

	if mock.chatCalls != 1 {
		t.Fatalf("expected 1 call, got %d", mock.chatCalls)
	}

	// Hit: should NOT call through again.
	mock.chatResp = &largemodel.Response{ID: "should-not-see"}

	resp, err = wrapped.Call(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "fresh" {
		t.Fatalf("expected cached ID 'fresh', got %q", resp.ID)
	}

	if mock.chatCalls != 1 {
		t.Fatalf("expected still 1 call (cached), got %d", mock.chatCalls)
	}
}

func TestCacheMiddleware_DifferentRequests(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{chatResp: &largemodel.Response{ID: "resp-1"}}

	wrapped := NewCacheMiddleware(cache).Wrap(mock)
	ctx := context.Background()

	req1 := &largemodel.Request{Model: "gpt-4", Messages: []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello"),
	}}
	req2 := &largemodel.Request{Model: "gpt-4", Messages: []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "world"),
	}}

	_, _ = wrapped.Call(ctx, req1)

	mock.chatResp = &largemodel.Response{ID: "resp-2"}

	resp, _ := wrapped.Call(ctx, req2)
	if resp.ID != "resp-2" {
		t.Fatalf("expected 'resp-2' for different request, got %q", resp.ID)
	}

	if mock.chatCalls != 2 {
		t.Fatalf("expected 2 calls (different keys), got %d", mock.chatCalls)
	}
}

func TestCacheMiddleware_Expiry(t *testing.T) {
	now := time.Now()
	currentTime := now

	cache := NewMapCache()
	cache.nowFn = func() time.Time { return currentTime }

	mock := &mockCompleter{chatResp: &largemodel.Response{ID: "v1"}}
	wrapped := NewCacheMiddleware(cache, WithCacheTTL(time.Minute)).Wrap(mock)

	ctx := context.Background()
	req := &largemodel.Request{Model: "gpt-4"}

	_, _ = wrapped.Call(ctx, req)

	// Advance past TTL.
	currentTime = now.Add(2 * time.Minute)
	mock.chatResp = &largemodel.Response{ID: "v2"}

	resp, _ := wrapped.Call(ctx, req)
	if resp.ID != "v2" {
		t.Fatalf("expected 'v2' after expiry, got %q", resp.ID)
	}

	if mock.chatCalls != 2 {
		t.Fatalf("expected 2 calls (expired cache), got %d", mock.chatCalls)
	}
}

func TestCacheMiddleware_StreamPassthrough(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{}
	wrapped := NewCacheMiddleware(cache).Wrap(mock)

	_, _ = wrapped.CallStream(context.Background(), &largemodel.Request{})

	if mock.streamCalls != 1 {
		t.Fatalf("expected 1 stream call, got %d", mock.streamCalls)
	}
}

func TestCacheMiddleware_ErrorNotCached(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{chatErr: largemodel.ErrEmptyResponse}
	wrapped := NewCacheMiddleware(cache).Wrap(mock)

	ctx := context.Background()
	req := &largemodel.Request{Model: "gpt-4"}

	_, err := wrapped.Call(ctx, req)
	if err == nil {
		t.Fatal("expected error")
	}

	// Try again; should call through (not cached).
	mock.chatErr = nil
	mock.chatResp = &largemodel.Response{ID: "ok"}

	resp, err := wrapped.Call(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "ok" {
		t.Fatalf("expected 'ok', got %q", resp.ID)
	}
}

func TestMapCache_GetMiss(t *testing.T) {
	c := NewMapCache()

	_, ok := c.Get(context.Background(), "nonexistent")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestCacheKey_Deterministic(t *testing.T) {
	req := &largemodel.Request{
		Model: "gpt-4",
		Messages: []schema.Message{
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "test"),
		},
	}

	k1, err := cacheKey(schema.ProtocolOpenAIChat, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(schema.ProtocolOpenAIChat, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k1 != k2 {
		t.Fatalf("cache keys should be deterministic: %q != %q", k1, k2)
	}

	if len(k1) != 64 {
		t.Fatalf("expected SHA-256 hex (64 chars), got %d chars", len(k1))
	}
}

func TestCacheKey_DifferentTemperature(t *testing.T) {
	base := []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello"),
	}

	temp1 := 0.2
	temp2 := 0.9

	req1 := &largemodel.Request{Model: "gpt-4", Messages: base, Temperature: &temp1}
	req2 := &largemodel.Request{Model: "gpt-4", Messages: base, Temperature: &temp2}

	k1, err := cacheKey(schema.ProtocolOpenAIChat, req1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(schema.ProtocolOpenAIChat, req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k1 == k2 {
		t.Fatal("requests with different Temperature must produce different cache keys")
	}
}

func TestMapCache_EvictsExpired(t *testing.T) {
	now := time.Now()
	currentTime := now

	c := NewMapCache()
	c.nowFn = func() time.Time { return currentTime }

	ctx := context.Background()
	ttl := time.Minute

	resp := func(id string) *largemodel.Response { return &largemodel.Response{ID: id} }

	c.Set(ctx, "key-a", resp("a"), ttl)
	c.Set(ctx, "key-b", resp("b"), ttl)
	c.Set(ctx, "key-c", resp("c"), ttl)

	// Advance time so key-a and key-b are expired; key-c is still live.
	currentTime = now.Add(2 * time.Minute)

	// Override only key-c's expiry by re-setting it at the new time.
	c.Set(ctx, "key-c", resp("c"), ttl)

	// Setting a new entry triggers lazy eviction of the two expired entries.
	c.Set(ctx, "key-d", resp("d"), ttl)

	c.mu.RLock()
	count := len(c.entries)
	_, hasA := c.entries["key-a"]
	_, hasB := c.entries["key-b"]
	c.mu.RUnlock()

	if count != 2 {
		t.Fatalf("expected 2 entries after eviction, got %d", count)
	}

	if hasA || hasB {
		t.Fatal("expired entries key-a and key-b should have been evicted")
	}
}

func TestMapCache_EvictsOldestAtCapacity(t *testing.T) {
	now := time.Now()
	tick := 0

	c := NewMapCache(WithMaxEntries(2))
	c.nowFn = func() time.Time {
		// Each call advances by one second so createdAt ordering is deterministic.
		t := now.Add(time.Duration(tick) * time.Second)
		tick++

		return t
	}

	ctx := context.Background()
	ttl := time.Hour

	c.Set(ctx, "oldest", &largemodel.Response{ID: "oldest"}, ttl)
	c.Set(ctx, "middle", &largemodel.Response{ID: "middle"}, ttl)
	// This third Set must evict the oldest entry to stay within maxEntries=2.
	c.Set(ctx, "newest", &largemodel.Response{ID: "newest"}, ttl)

	c.mu.RLock()
	count := len(c.entries)
	_, hasOldest := c.entries["oldest"]
	c.mu.RUnlock()

	if count != 2 {
		t.Fatalf("expected 2 entries after capacity eviction, got %d", count)
	}

	if hasOldest {
		t.Fatal("oldest entry should have been evicted when cache exceeded capacity")
	}
}

func TestNewMapCache_BackwardCompatible(t *testing.T) {
	c := NewMapCache()

	if c == nil {
		t.Fatal("NewMapCache() returned nil")
	}

	if c.maxEntries != defaultMaxEntries {
		t.Fatalf("expected default maxEntries %d, got %d", defaultMaxEntries, c.maxEntries)
	}

	// Verify the cache is usable end-to-end without options.
	ctx := context.Background()
	resp := &largemodel.Response{ID: "test"}
	c.Set(ctx, "k", resp, time.Minute)

	got, ok := c.Get(ctx, "k")
	if !ok {
		t.Fatal("expected cache hit")
	}

	if got.ID != "test" {
		t.Fatalf("expected ID 'test', got %q", got.ID)
	}
}

// TestCacheKey_DifferentResponseSchema pins that ResponseSchema participates
// in the cache key: two requests that differ only in the requested output
// shape must not collide, and either must differ from the schema-less
// request that keeps today's key.
func TestCacheKey_DifferentResponseSchema(t *testing.T) {
	base := []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello"),
	}

	reqNone := &largemodel.Request{Model: "gpt-4", Messages: base}
	reqA := &largemodel.Request{Model: "gpt-4", Messages: base, ResponseSchema: map[string]any{"type": "object"}}
	reqB := &largemodel.Request{Model: "gpt-4", Messages: base, ResponseSchema: map[string]any{"type": "string"}}

	kNone, err := cacheKey(schema.ProtocolOpenAIChat, reqNone)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kA, err := cacheKey(schema.ProtocolOpenAIChat, reqA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kB, err := cacheKey(schema.ProtocolOpenAIChat, reqB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if kA == kB {
		t.Fatal("requests with different ResponseSchema must produce different cache keys")
	}

	if kA == kNone || kB == kNone {
		t.Fatal("a ResponseSchema request must not collide with the schema-less request")
	}
}

// TestCacheKey_DifferentProtocol pins the dual-track cache rule: the same
// conversation addressed to two protocols is two distinct calls, because the
// stored messages are vendor-native wire forms.
func TestCacheKey_DifferentProtocol(t *testing.T) {
	base := []schema.Message{
		schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello"),
	}

	req1 := &largemodel.Request{Model: "gpt-4", Messages: base}
	req2 := &largemodel.Request{Model: "gpt-4", Messages: base}

	k1, err := cacheKey(schema.ProtocolOpenAIChat, req1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(schema.ProtocolAnthropicMessages, req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k1 == k2 {
		t.Fatal("requests under different protocols must produce different cache keys")
	}
}

func TestCacheKey_FormalFieldsAndExtensions(t *testing.T) {
	base := []schema.Message{schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hello")}
	topP := 0.5
	reqA := &largemodel.Request{Model: "gpt-4", Messages: base, TopP: &topP}
	reqB := &largemodel.Request{Model: "gpt-4", Messages: base}
	reqC := &largemodel.Request{Model: "gpt-4", Messages: base, ProviderExtensions: map[string]any{"openais": map[string]any{"x": 1}}}

	kA, err := cacheKey(schema.ProtocolOpenAIChat, reqA)
	if err != nil {
		t.Fatal(err)
	}

	kB, err := cacheKey(schema.ProtocolOpenAIChat, reqB)
	if err != nil {
		t.Fatal(err)
	}

	kC, err := cacheKey(schema.ProtocolOpenAIChat, reqC)
	if err != nil {
		t.Fatal(err)
	}

	if kA == kB || kB == kC || kA == kC {
		t.Fatal("different formal fields or extensions must not share a cache key")
	}
}
