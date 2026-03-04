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
	"strings"
	"time"
)

// mapStore is the common map-based storage logic shared by all memory tiers.
// It is not safe for concurrent use; callers must provide their own locking.
type mapStore struct {
	entries   map[string]*Entry
	scope     Scope
	agentID   string
	sessionID string
}

func (s *mapStore) get(key string) (any, bool) {
	e, ok := s.entries[key]
	if !ok {
		return nil, false
	}

	if e.IsExpired() {
		delete(s.entries, key)
		return nil, false
	}

	return e.Value, true
}

func (s *mapStore) set(key string, value any, ttl int64) {
	s.entries[key] = &Entry{
		Key:       key,
		Value:     value,
		Scope:     s.scope,
		AgentID:   s.agentID,
		SessionID: s.sessionID,
		CreatedAt: time.Now(),
		TTL:       ttl,
	}
}

func (s *mapStore) del(key string) {
	delete(s.entries, key)
}

func (s *mapStore) list(prefix string) []Entry {
	result := make([]Entry, 0, len(s.entries))

	for k, e := range s.entries {
		if e.IsExpired() {
			delete(s.entries, k)
			continue
		}

		if strings.HasPrefix(k, prefix) {
			result = append(result, *e)
		}
	}

	return result
}

func (s *mapStore) clear() {
	s.entries = make(map[string]*Entry)
}

func (s *mapStore) batchGet(keys []string) map[string]any {
	result := make(map[string]any, len(keys))

	for _, key := range keys {
		if v, ok := s.get(key); ok {
			result[key] = v
		}
	}

	return result
}

func (s *mapStore) batchSet(entries map[string]any, ttl int64) {
	now := time.Now()

	for key, value := range entries {
		s.entries[key] = &Entry{
			Key:       key,
			Value:     value,
			Scope:     s.scope,
			AgentID:   s.agentID,
			SessionID: s.sessionID,
			CreatedAt: now,
			TTL:       ttl,
		}
	}
}
