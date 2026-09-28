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

package skill

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/vogo/largemodel/schema"
)

// EventDispatcher is a callback for dispatching events without importing the hook package.
type EventDispatcher func(ctx context.Context, event schema.Event)

// Manager manages skill activations per session.
type Manager interface {
	Activate(ctx context.Context, name string, sessionID string) (*Activation, error)
	Deactivate(ctx context.Context, name string, sessionID string) error
	ActiveSkills(sessionID string) []*Activation
	ClearSession(ctx context.Context, sessionID string)
	LoadResource(ctx context.Context, sessionID string, skillName string, resourceType string, resourceName string) (*Resource, error)
}

// ManagerOption configures an InMemoryManager during construction.
type ManagerOption func(*InMemoryManager)

// WithEventDispatcher sets the optional event dispatcher callback.
func WithEventDispatcher(d EventDispatcher) ManagerOption {
	return func(m *InMemoryManager) { m.dispatcher = d }
}

// InMemoryManager is an in-memory implementation of Manager.
type InMemoryManager struct {
	registry    Registry
	mu          sync.RWMutex
	activations map[string][]*Activation // keyed by sessionID
	dispatcher  EventDispatcher
}

// Compile-time check: InMemoryManager implements Manager.
var _ Manager = (*InMemoryManager)(nil)

// NewManager creates a new InMemoryManager with the given registry and options.
func NewManager(registry Registry, opts ...ManagerOption) *InMemoryManager {
	m := &InMemoryManager{
		registry:    registry,
		activations: make(map[string][]*Activation),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Activate activates a skill for the given session.
func (m *InMemoryManager) Activate(ctx context.Context, name string, sessionID string) (*Activation, error) {
	def, ok := m.registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("skill %q not found", name)
	}

	// Make a copy to prevent external mutation of the registry's definition.
	defCopy := *def

	activation := &Activation{
		SkillName:   name,
		SessionID:   sessionID,
		ActivatedAt: time.Now(),
		def:         &defCopy,
	}

	m.mu.Lock()
	// Check for duplicate activation.
	for _, a := range m.activations[sessionID] {
		if a.SkillName == name {
			m.mu.Unlock()
			return nil, fmt.Errorf("skill %q already active for session %q", name, sessionID)
		}
	}
	m.activations[sessionID] = append(m.activations[sessionID], activation)
	m.mu.Unlock()

	m.dispatchEvent(ctx, schema.EventSkillActivate, schema.SkillActivateData{
		SkillName: name,
		SessionID: sessionID,
	})

	return activation, nil
}

// Deactivate removes a skill activation from the given session.
func (m *InMemoryManager) Deactivate(ctx context.Context, name string, sessionID string) error {
	m.mu.Lock()
	acts := m.activations[sessionID]
	found := false
	for i, a := range acts {
		if a.SkillName == name {
			m.activations[sessionID] = append(acts[:i], acts[i+1:]...)
			found = true
			break
		}
	}
	m.mu.Unlock()

	if !found {
		return fmt.Errorf("skill %q not active for session %q", name, sessionID)
	}

	m.dispatchEvent(ctx, schema.EventSkillDeactivate, schema.SkillDeactivateData{
		SkillName: name,
		SessionID: sessionID,
	})

	return nil
}

// ActiveSkills returns all active skill activations for the given session.
func (m *InMemoryManager) ActiveSkills(sessionID string) []*Activation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	acts := m.activations[sessionID]
	if len(acts) == 0 {
		return nil
	}

	result := make([]*Activation, len(acts))
	copy(result, acts)
	return result
}

// ClearSession removes all skill activations for the given session.
func (m *InMemoryManager) ClearSession(ctx context.Context, sessionID string) {
	m.mu.Lock()
	acts := m.activations[sessionID]
	delete(m.activations, sessionID)
	m.mu.Unlock()

	for _, a := range acts {
		m.dispatchEvent(ctx, schema.EventSkillDeactivate, schema.SkillDeactivateData{
			SkillName: a.SkillName,
			SessionID: sessionID,
		})
	}
}

// LoadResource loads a resource file for an active skill.
func (m *InMemoryManager) LoadResource(ctx context.Context, sessionID string, skillName string, resourceType string, resourceName string) (*Resource, error) {
	m.mu.RLock()
	var activation *Activation
	for _, a := range m.activations[sessionID] {
		if a.SkillName == skillName {
			activation = a
			break
		}
	}
	m.mu.RUnlock()

	if activation == nil {
		return nil, fmt.Errorf("skill %q not active for session %q", skillName, sessionID)
	}

	// Find the resource in the skill definition.
	var resource *Resource
	for i := range activation.def.Resources {
		r := &activation.def.Resources[i]
		if r.Type == resourceType && r.Name == resourceName {
			resource = r
			break
		}
	}

	if resource == nil {
		return nil, fmt.Errorf("resource %s/%s not found in skill %q", resourceType, resourceName, skillName)
	}

	data, err := os.ReadFile(resource.Path)
	if err != nil {
		return nil, fmt.Errorf("read resource %q: %w", resource.Path, err)
	}

	result := &Resource{
		Type:    resource.Type,
		Name:    resource.Name,
		Path:    resource.Path,
		Content: string(data),
	}

	m.dispatchEvent(ctx, schema.EventSkillResourceLoad, schema.SkillResourceLoadData{
		SkillName:    skillName,
		ResourceType: resourceType,
		ResourceName: resourceName,
	})

	return result, nil
}

// dispatchEvent sends an event via the dispatcher if configured.
func (m *InMemoryManager) dispatchEvent(ctx context.Context, eventType string, data schema.EventData) {
	if m.dispatcher != nil {
		m.dispatcher(ctx, schema.NewEvent(eventType, "", "", data))
	}
}
