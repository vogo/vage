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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/vogo/vage/largemodel"

	"github.com/vogo/vage/schema"
)

const defaultCacheTTL = 5 * time.Minute

// Cache stores and retrieves chat completion responses.
type Cache interface {
	Get(ctx context.Context, key string) (*largemodel.Response, bool)
	Set(ctx context.Context, key string, resp *largemodel.Response, ttl time.Duration)
}

// CacheMiddleware caches Call responses.
// Stream calls are passed through without caching.
type CacheMiddleware struct {
	cache Cache
	ttl   time.Duration
}

// CacheOption configures CacheMiddleware.
type CacheOption func(*CacheMiddleware)

// WithCacheTTL sets the cache time-to-live.
func WithCacheTTL(d time.Duration) CacheOption {
	return func(m *CacheMiddleware) { m.ttl = d }
}

// NewCacheMiddleware creates a CacheMiddleware with the given cache and options.
func NewCacheMiddleware(c Cache, opts ...CacheOption) *CacheMiddleware {
	m := &CacheMiddleware{cache: c, ttl: defaultCacheTTL}
	for _, o := range opts {
		o(m)
	}

	return m
}

// Wrap implements Middleware.
func (m *CacheMiddleware) Wrap(next largemodel.Caller) largemodel.Caller {
	return &largemodel.CallerFunc{
		Proto: next.Protocol(),
		Chat: func(ctx context.Context, req *largemodel.Request) (*largemodel.Response, error) {
			key, err := cacheKey(next.Protocol(), req)
			if err != nil {
				// Skip cache on marshal failure; call downstream directly.
				return next.Call(ctx, req)
			}

			if resp, ok := m.cache.Get(ctx, key); ok {
				return resp, nil
			}

			resp, err := next.Call(ctx, req)
			if err != nil {
				return nil, err
			}

			m.cache.Set(ctx, key, resp, m.ttl)

			return resp, nil
		},
		ChatStream: func(ctx context.Context, req *largemodel.Request) (*largemodel.Stream, error) {
			return next.CallStream(ctx, req)
		},
	}
}

// cacheKeyData is the deterministic subset of a largemodel.Request used for cache keys.
// Every field that influences model output must be included here.
//
// Protocol is part of the key because messages are stored in vendor-native
// wire form: the same conversation addressed to two protocols is two distinct
// calls with two distinct responses.
type cacheKeyData struct {
	Protocol       schema.Protocol  `json:"protocol"`
	Model          string           `json:"model"`
	Messages       []schema.Message `json:"messages"`
	Tools          []schema.ToolDef `json:"tools,omitempty"`
	Temperature    *float64         `json:"temperature,omitempty"`
	MaxTokens      *int             `json:"max_tokens,omitempty"`
	Stop           []string         `json:"stop,omitempty"`
	PromptCaching  bool             `json:"prompt_caching,omitempty"`
	ResponseSchema any              `json:"response_schema,omitempty"`
}

// cacheKey produces a SHA-256 hex digest from the deterministic request fields.
func cacheKey(proto schema.Protocol, req *largemodel.Request) (string, error) {
	data := cacheKeyData{
		Protocol:       proto,
		Model:          req.Model,
		Messages:       req.Messages,
		Tools:          req.Tools,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		Stop:           req.Stop,
		PromptCaching:  req.PromptCaching,
		ResponseSchema: req.ResponseSchema,
	}

	b, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("largemodel: failed to marshal cache key: %w", err)
	}

	h := sha256.Sum256(b)

	return fmt.Sprintf("%x", h), nil
}

const defaultMaxEntries = 1000

// MapCache is a thread-safe in-memory Cache implementation.
type MapCache struct {
	mu         sync.RWMutex
	entries    map[string]cacheEntry
	maxEntries int
	nowFn      func() time.Time
}

type cacheEntry struct {
	resp      *largemodel.Response
	expiresAt time.Time
	createdAt time.Time
}

// MapCacheOption configures MapCache.
type MapCacheOption func(*MapCache)

// WithMaxEntries sets the maximum number of entries in the cache.
func WithMaxEntries(n int) MapCacheOption {
	return func(c *MapCache) { c.maxEntries = n }
}

// NewMapCache creates an empty MapCache with optional configuration.
func NewMapCache(opts ...MapCacheOption) *MapCache {
	c := &MapCache{
		entries:    make(map[string]cacheEntry),
		maxEntries: defaultMaxEntries,
		nowFn:      time.Now,
	}
	for _, o := range opts {
		o(c)
	}

	return c
}

// Get returns a cached response if present and not expired.
func (c *MapCache) Get(_ context.Context, key string) (*largemodel.Response, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}

	if c.nowFn().After(e.expiresAt) {
		return nil, false
	}

	return e.resp, true
}

// Set stores a response with the given TTL.
// It lazily removes expired entries and evicts the oldest entry when the cache
// is at capacity before inserting the new one.
func (c *MapCache) Set(_ context.Context, key string, resp *largemodel.Response, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.nowFn()

	// Lazy cleanup: remove expired entries.
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}

	// Evict oldest entry if at capacity.
	if c.maxEntries > 0 && len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldestTime time.Time

		for k, e := range c.entries {
			if oldestKey == "" || e.createdAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = e.createdAt
			}
		}

		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}

	c.entries[key] = cacheEntry{
		resp:      resp,
		expiresAt: now.Add(ttl),
		createdAt: now,
	}
}
