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

package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/schema"
)

type entry struct {
	def     schema.ToolDef
	handler ToolHandler // nil for external (MCP) tools
}

// Registry is a thread-safe in-memory tool registry.
type Registry struct {
	mu             sync.RWMutex
	entries        map[string]*entry
	externalCaller ExternalToolCaller
}

// Compile-time check: Registry implements ToolRegistry.
var _ ToolRegistry = (*Registry)(nil)

// RegistryOption configures a Registry during construction.
type RegistryOption func(*Registry)

// WithExternalCaller sets the caller used for tools with no local handler.
func WithExternalCaller(c ExternalToolCaller) RegistryOption {
	return func(r *Registry) { r.externalCaller = c }
}

// NewRegistry creates an empty Registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{entries: make(map[string]*entry)}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *Registry) Register(def schema.ToolDef, handler ToolHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[def.Name] = &entry{def: def, handler: handler}
	return nil
}

// RegisterIfAbsent atomically checks for duplicates and registers under a single write lock
// to avoid a TOCTOU race between a separate read-lock check and Register().
func (r *Registry) RegisterIfAbsent(def schema.ToolDef, handler ToolHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[def.Name]; exists {
		return fmt.Errorf("tool %q already registered", def.Name)
	}

	r.entries[def.Name] = &entry{def: def, handler: handler}

	return nil
}

func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, name)
	return nil
}

func (r *Registry) Get(name string) (schema.ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return schema.ToolDef{}, false
	}
	return e.def, true
}

func (r *Registry) List() []schema.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]schema.ToolDef, 0, len(r.entries))
	for _, e := range r.entries {
		defs = append(defs, e.def)
	}
	return defs
}

func (r *Registry) Merge(defs []schema.ToolDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		if _, exists := r.entries[d.Name]; !exists {
			r.entries[d.Name] = &entry{def: d}
		}
	}
}

// SetExternalCaller sets the caller used for tools with no local handler.
// Prefer WithExternalCaller at construction time when possible.
func (r *Registry) SetExternalCaller(c ExternalToolCaller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.externalCaller = c
}

func (r *Registry) Execute(ctx context.Context, name, args string) (schema.ToolResult, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	extCaller := r.externalCaller
	r.mu.RUnlock()
	if !ok {
		return schema.ToolResult{}, fmt.Errorf("tool %q not found", name)
	}
	if e.handler != nil {
		return e.handler(ctx, name, args)
	}
	if extCaller != nil {
		return extCaller.CallTool(ctx, name, args)
	}
	return schema.ToolResult{}, fmt.Errorf("tool %q has no handler", name)
}

// ToAIModelTools converts tool definitions to the aimodel.Tool format for ChatRequest.
func ToAIModelTools(defs []schema.ToolDef) []aimodel.Tool {
	tools := make([]aimodel.Tool, len(defs))
	for i, d := range defs {
		tools[i] = aimodel.Tool{
			Type: "function",
			Function: aimodel.FunctionDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		}
	}
	return tools
}

// FilterTools returns only the tools whose names are in the whitelist.
// If names is empty, all tools are returned.
func FilterTools(defs []schema.ToolDef, names []string) []schema.ToolDef {
	if len(names) == 0 {
		return defs
	}
	allow := make(map[string]struct{}, len(names))
	for _, n := range names {
		allow[n] = struct{}{}
	}
	filtered := make([]schema.ToolDef, 0, len(names))
	for _, d := range defs {
		if _, ok := allow[d.Name]; ok {
			filtered = append(filtered, d)
		}
	}
	return filtered
}
