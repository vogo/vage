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
	"testing"
	"time"

	"github.com/vogo/aimodel"
)

func TestCacheMiddleware_HitAndMiss(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "fresh"}}

	wrapped := NewCacheMiddleware(cache, WithCacheTTL(time.Minute)).Wrap(mock)
	ctx := context.Background()
	req := &aimodel.ChatRequest{Model: "gpt-4", Messages: []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}}

	// Miss: should call through.
	resp, err := wrapped.ChatCompletion(ctx, req)
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
	mock.chatResp = &aimodel.ChatResponse{ID: "should-not-see"}

	resp, err = wrapped.ChatCompletion(ctx, req)
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
	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "resp-1"}}

	wrapped := NewCacheMiddleware(cache).Wrap(mock)
	ctx := context.Background()

	req1 := &aimodel.ChatRequest{Model: "gpt-4", Messages: []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}}
	req2 := &aimodel.ChatRequest{Model: "gpt-4", Messages: []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("world")},
	}}

	_, _ = wrapped.ChatCompletion(ctx, req1)

	mock.chatResp = &aimodel.ChatResponse{ID: "resp-2"}

	resp, _ := wrapped.ChatCompletion(ctx, req2)
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

	mock := &mockCompleter{chatResp: &aimodel.ChatResponse{ID: "v1"}}
	wrapped := NewCacheMiddleware(cache, WithCacheTTL(time.Minute)).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{Model: "gpt-4"}

	_, _ = wrapped.ChatCompletion(ctx, req)

	// Advance past TTL.
	currentTime = now.Add(2 * time.Minute)
	mock.chatResp = &aimodel.ChatResponse{ID: "v2"}

	resp, _ := wrapped.ChatCompletion(ctx, req)
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

	_, _ = wrapped.ChatCompletionStream(context.Background(), &aimodel.ChatRequest{})

	if mock.streamCalls != 1 {
		t.Fatalf("expected 1 stream call, got %d", mock.streamCalls)
	}
}

func TestCacheMiddleware_ErrorNotCached(t *testing.T) {
	cache := NewMapCache()
	mock := &mockCompleter{chatErr: aimodel.ErrEmptyResponse}
	wrapped := NewCacheMiddleware(cache).Wrap(mock)

	ctx := context.Background()
	req := &aimodel.ChatRequest{Model: "gpt-4"}

	_, err := wrapped.ChatCompletion(ctx, req)
	if err == nil {
		t.Fatal("expected error")
	}

	// Try again; should call through (not cached).
	mock.chatErr = nil
	mock.chatResp = &aimodel.ChatResponse{ID: "ok"}

	resp, err := wrapped.ChatCompletion(ctx, req)
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
	req := &aimodel.ChatRequest{
		Model: "gpt-4",
		Messages: []aimodel.Message{
			{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("test")},
		},
	}

	k1, err := cacheKey(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(req)
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
	base := []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}

	temp1 := 0.2
	temp2 := 0.9

	req1 := &aimodel.ChatRequest{Model: "gpt-4", Messages: base, Temperature: &temp1}
	req2 := &aimodel.ChatRequest{Model: "gpt-4", Messages: base, Temperature: &temp2}

	k1, err := cacheKey(req1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(req2)
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

	resp := func(id string) *aimodel.ChatResponse { return &aimodel.ChatResponse{ID: id} }

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

	c.Set(ctx, "oldest", &aimodel.ChatResponse{ID: "oldest"}, ttl)
	c.Set(ctx, "middle", &aimodel.ChatResponse{ID: "middle"}, ttl)
	// This third Set must evict the oldest entry to stay within maxEntries=2.
	c.Set(ctx, "newest", &aimodel.ChatResponse{ID: "newest"}, ttl)

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
	resp := &aimodel.ChatResponse{ID: "test"}
	c.Set(ctx, "k", resp, time.Minute)

	got, ok := c.Get(ctx, "k")
	if !ok {
		t.Fatal("expected cache hit")
	}

	if got.ID != "test" {
		t.Fatalf("expected ID 'test', got %q", got.ID)
	}
}

func TestCacheKey_DifferentSeed(t *testing.T) {
	base := []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}

	seed1 := 42
	seed2 := 99

	req1 := &aimodel.ChatRequest{Model: "gpt-4", Messages: base, Seed: &seed1}
	req2 := &aimodel.ChatRequest{Model: "gpt-4", Messages: base, Seed: &seed2}

	k1, err := cacheKey(req1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	k2, err := cacheKey(req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k1 == k2 {
		t.Fatal("requests with different Seed must produce different cache keys")
	}
}
