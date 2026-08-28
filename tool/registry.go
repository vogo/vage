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
	"slices"
	"sort"
	"sync"

	"github.com/vogo/vage/schema"
)

type entry struct {
	def     schema.ToolDef
	handler ToolHandler // nil for external (MCP) tools
}

// ExecuteMiddleware wraps the Registry execute path so callers can observe,
// short-circuit, or rewrite a tool call before and after dispatch. next is
// either the next middleware or the base lookup/dispatch handler.
//
// The first registered middleware is outermost: for A then B the call order is
// A.before → B.before → dispatch → B.after → A.after. A middleware may skip
// next (deny or synthesise a result) or call next with a different name/args;
// the framework does not normalise returns or recover panics.
//
// Concurrent Execute calls share the same middleware instances; stateful
// middlewares must be safe for concurrent use. Direct ToolHandler calls bypass
// this chain — enforce policies by constructing the Registry with them.
type ExecuteMiddleware func(next ToolHandler) ToolHandler

// Registry is a thread-safe in-memory tool registry.
type Registry struct {
	mu                 sync.RWMutex
	entries            map[string]*entry
	externalCaller     ExternalToolCaller
	executeMiddlewares []ExecuteMiddleware
	// execute is the fixed middleware chain around dispatch, built once in
	// NewRegistry. dispatch itself still reads live registry state on each call.
	execute ToolHandler
}

// Compile-time check: Registry implements ToolRegistry.
var _ ToolRegistry = (*Registry)(nil)

// RegistryOption configures a Registry during construction.
type RegistryOption func(*Registry)

// WithExternalCaller sets the caller used for tools with no local handler.
func WithExternalCaller(c ExternalToolCaller) RegistryOption {
	return func(r *Registry) { r.externalCaller = c }
}

// WithExecuteMiddleware appends execute middlewares in registration order.
// Repeated calls append; nil entries are skipped so callers can assemble the
// chain conditionally without compaction.
func WithExecuteMiddleware(middlewares ...ExecuteMiddleware) RegistryOption {
	return func(r *Registry) {
		for _, mw := range middlewares {
			if mw != nil {
				r.executeMiddlewares = append(r.executeMiddlewares, mw)
			}
		}
	}
}

// NewRegistry creates an empty Registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{entries: make(map[string]*entry)}
	for _, o := range opts {
		o(r)
	}
	r.execute = chainExecuteMiddleware(r.dispatch, r.executeMiddlewares...)
	return r
}

// chainExecuteMiddleware applies middlewares around base so that
// middlewares[0] is outermost. nil entries are skipped.
func chainExecuteMiddleware(base ToolHandler, middlewares ...ExecuteMiddleware) ToolHandler {
	wrapped := base
	for _, mw := range slices.Backward(middlewares) {
		if mw == nil {
			continue
		}
		wrapped = mw(wrapped)
	}
	return wrapped
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

// List returns all registered tool definitions in a deterministic
// (name-sorted) order. Stable ordering keeps the Anthropic prompt-cache
// prefix (tools block) byte-identical across independent invocations,
// which is a prerequisite for cache hits — map iteration order would
// otherwise shuffle the prefix on every call.
func (r *Registry) List() []schema.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]schema.ToolDef, 0, len(r.entries))
	for _, e := range r.entries {
		defs = append(defs, e.def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
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
	return r.execute(ctx, name, args)
}

// dispatch looks up the tool and runs the local handler or external caller.
// The Registry lock is held only while taking the lookup snapshot — never
// across middleware, handler, or external call execution.
func (r *Registry) dispatch(ctx context.Context, name, args string) (schema.ToolResult, error) {
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
