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
	"fmt"
)

// Manager orchestrates the three-tier memory system.
type Manager struct {
	session    Memory
	store      Memory
	promoter   Promoter
	archiver   Archiver
	compressor ContextCompressor
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithSession sets the session-tier memory.
func WithSession(m Memory) ManagerOption {
	return func(mgr *Manager) { mgr.session = m }
}

// WithStore sets the store-tier memory.
func WithStore(m Memory) ManagerOption {
	return func(mgr *Manager) { mgr.store = m }
}

// WithPromoter sets the promotion strategy.
func WithPromoter(p Promoter) ManagerOption {
	return func(mgr *Manager) { mgr.promoter = p }
}

// WithArchiver sets the archival strategy.
func WithArchiver(a Archiver) ManagerOption {
	return func(mgr *Manager) { mgr.archiver = a }
}

// WithCompressor sets the context compression strategy.
func WithCompressor(c ContextCompressor) ManagerOption {
	return func(mgr *Manager) { mgr.compressor = c }
}

// NewManager creates a Manager with the given options.
// Defaults: PromoteAll promoter, ArchiveNone archiver.
func NewManager(opts ...ManagerOption) *Manager {
	mgr := &Manager{
		promoter: PromoteAll(),
		archiver: ArchiveNone(),
	}

	for _, o := range opts {
		o(mgr)
	}

	return mgr
}

// Session returns the session-tier memory, or nil if not configured.
func (mgr *Manager) Session() Memory {
	return mgr.session
}

// Store returns the store-tier memory, or nil if not configured.
func (mgr *Manager) Store() Memory {
	return mgr.store
}

// Compressor returns the context compressor, or nil if not configured.
func (mgr *Manager) Compressor() ContextCompressor {
	return mgr.compressor
}

// PromoteToSession promotes entries from working memory to session memory.
// Returns an error if promotion fails, but callers should not fail the Run.
func (mgr *Manager) PromoteToSession(ctx context.Context, working *WorkingMemory) error {
	if mgr.session == nil || mgr.promoter == nil {
		return nil
	}

	entries, err := working.List(ctx, "")
	if err != nil {
		return fmt.Errorf("memory: list working entries: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	promoted, err := mgr.promoter.Promote(ctx, entries)
	if err != nil {
		return fmt.Errorf("memory: promote entries: %w", err)
	}

	for i := range promoted {
		e := &promoted[i]
		if err := mgr.session.Set(ctx, e.Key, e.Value, e.TTL); err != nil {
			return fmt.Errorf("memory: write to session %q: %w", e.Key, err)
		}
	}

	return nil
}

// ArchiveToStore archives entries from session memory to store memory.
// Returns an error if archival fails, but callers should not fail the Run.
func (mgr *Manager) ArchiveToStore(ctx context.Context) error {
	if mgr.session == nil || mgr.store == nil || mgr.archiver == nil {
		return nil
	}

	entries, err := mgr.session.List(ctx, "")
	if err != nil {
		return fmt.Errorf("memory: list session entries: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	archived, err := mgr.archiver.Archive(ctx, entries)
	if err != nil {
		return fmt.Errorf("memory: archive entries: %w", err)
	}

	for i := range archived {
		e := &archived[i]
		if err := mgr.store.Set(ctx, e.Key, e.Value, e.TTL); err != nil {
			return fmt.Errorf("memory: write to store %q: %w", e.Key, err)
		}
	}

	return nil
}
