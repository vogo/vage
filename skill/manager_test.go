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
	"sync"
	"testing"

	"github.com/vogo/largemodel/schema"
)

func setupTestRegistry(t *testing.T) Registry {
	t.Helper()
	r := NewRegistry()
	loader := &FileLoader{}
	ctx := context.Background()

	def, err := loader.Load(ctx, "testdata/valid-skill")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := r.Register(def); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	defMin, err := loader.Load(ctx, "testdata/minimal-skill")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := r.Register(defMin); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	return r
}

func TestManager_ActivateDeactivate(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	// Activate.
	act, err := mgr.Activate(ctx, "valid-skill", "sess-1")
	if err != nil {
		t.Fatalf("Activate error: %v", err)
	}
	if act.SkillName != "valid-skill" {
		t.Errorf("SkillName = %q, want %q", act.SkillName, "valid-skill")
	}
	if act.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", act.SessionID, "sess-1")
	}
	def := act.SkillDef()
	if def.Name == "" {
		t.Fatal("SkillDef should not be empty")
	}

	// ActiveSkills should include it.
	active := mgr.ActiveSkills("sess-1")
	if len(active) != 1 {
		t.Fatalf("ActiveSkills length = %d, want 1", len(active))
	}

	// Deactivate.
	if err := mgr.Deactivate(ctx, "valid-skill", "sess-1"); err != nil {
		t.Fatalf("Deactivate error: %v", err)
	}

	active = mgr.ActiveSkills("sess-1")
	if len(active) != 0 {
		t.Errorf("ActiveSkills length = %d, want 0", len(active))
	}
}

func TestManager_ActivateNotFound(t *testing.T) {
	reg := NewRegistry()
	mgr := NewManager(reg)

	_, err := mgr.Activate(context.Background(), "nonexistent", "sess-1")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestManager_ActivateDuplicate(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	_, err := mgr.Activate(ctx, "valid-skill", "sess-1")
	if err != nil {
		t.Fatalf("first Activate error: %v", err)
	}

	_, err = mgr.Activate(ctx, "valid-skill", "sess-1")
	if err == nil {
		t.Fatal("expected error for duplicate activation")
	}
}

func TestManager_DeactivateNotActive(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)

	err := mgr.Deactivate(context.Background(), "valid-skill", "sess-1")
	if err == nil {
		t.Fatal("expected error for deactivating inactive skill")
	}
}

func TestManager_SessionIsolation(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	_, _ = mgr.Activate(ctx, "valid-skill", "sess-1")
	_, _ = mgr.Activate(ctx, "minimal-skill", "sess-2")

	s1 := mgr.ActiveSkills("sess-1")
	s2 := mgr.ActiveSkills("sess-2")

	if len(s1) != 1 || s1[0].SkillName != "valid-skill" {
		t.Errorf("sess-1 active = %v", s1)
	}
	if len(s2) != 1 || s2[0].SkillName != "minimal-skill" {
		t.Errorf("sess-2 active = %v", s2)
	}
}

func TestManager_ActiveSkillsEmpty(t *testing.T) {
	reg := NewRegistry()
	mgr := NewManager(reg)

	active := mgr.ActiveSkills("nonexistent")
	if active != nil {
		t.Errorf("ActiveSkills = %v, want nil", active)
	}
}

func TestManager_ClearSession(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	_, _ = mgr.Activate(ctx, "valid-skill", "sess-1")
	_, _ = mgr.Activate(ctx, "minimal-skill", "sess-1")

	if active := mgr.ActiveSkills("sess-1"); len(active) != 2 {
		t.Fatalf("ActiveSkills before clear = %d, want 2", len(active))
	}

	mgr.ClearSession(ctx, "sess-1")

	if active := mgr.ActiveSkills("sess-1"); len(active) != 0 {
		t.Errorf("ActiveSkills after clear = %d, want 0", len(active))
	}

	// Clearing an empty session should not panic.
	mgr.ClearSession(ctx, "nonexistent")
}

func TestManager_LoadResource(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	_, err := mgr.Activate(ctx, "valid-skill", "sess-1")
	if err != nil {
		t.Fatalf("Activate error: %v", err)
	}

	res, err := mgr.LoadResource(ctx, "sess-1", "valid-skill", ResourceTypeScript, "build.sh")
	if err != nil {
		t.Fatalf("LoadResource error: %v", err)
	}
	if res.Content == "" {
		t.Error("Content should not be empty")
	}
	if res.Type != ResourceTypeScript {
		t.Errorf("Type = %q, want %q", res.Type, ResourceTypeScript)
	}
	if res.Name != "build.sh" {
		t.Errorf("Name = %q, want %q", res.Name, "build.sh")
	}

	// Second load should also succeed (no cache, but reads from disk again).
	res2, err := mgr.LoadResource(ctx, "sess-1", "valid-skill", ResourceTypeScript, "build.sh")
	if err != nil {
		t.Fatalf("LoadResource (second) error: %v", err)
	}
	if res2.Content != res.Content {
		t.Error("second load content should match")
	}
}

func TestManager_LoadResource_NotActive(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)

	_, err := mgr.LoadResource(context.Background(), "sess-1", "valid-skill", ResourceTypeScript, "build.sh")
	if err == nil {
		t.Fatal("expected error for loading resource from inactive skill")
	}
}

func TestManager_LoadResource_NotFound(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	_, _ = mgr.Activate(ctx, "valid-skill", "sess-1")

	_, err := mgr.LoadResource(ctx, "sess-1", "valid-skill", ResourceTypeScript, "nonexistent.sh")
	if err == nil {
		t.Fatal("expected error for nonexistent resource")
	}
}

func TestManager_EventDispatcher(t *testing.T) {
	reg := setupTestRegistry(t)

	var events []schema.Event
	dispatcher := func(_ context.Context, event schema.Event) {
		events = append(events, event)
	}

	mgr := NewManager(reg, WithEventDispatcher(dispatcher))
	ctx := context.Background()

	_, _ = mgr.Activate(ctx, "valid-skill", "sess-1")
	_, _ = mgr.LoadResource(ctx, "sess-1", "valid-skill", ResourceTypeScript, "build.sh")
	_ = mgr.Deactivate(ctx, "valid-skill", "sess-1")

	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3", len(events))
	}

	expectedTypes := []string{schema.EventSkillActivate, schema.EventSkillResourceLoad, schema.EventSkillDeactivate}
	for i, want := range expectedTypes {
		if events[i].Type != want {
			t.Errorf("events[%d].Type = %q, want %q", i, events[i].Type, want)
		}
	}
}

func TestManager_ClearSession_Events(t *testing.T) {
	reg := setupTestRegistry(t)

	var events []schema.Event
	dispatcher := func(_ context.Context, event schema.Event) {
		events = append(events, event)
	}

	mgr := NewManager(reg, WithEventDispatcher(dispatcher))
	ctx := context.Background()

	_, _ = mgr.Activate(ctx, "valid-skill", "sess-1")
	_, _ = mgr.Activate(ctx, "minimal-skill", "sess-1")
	events = nil // reset

	mgr.ClearSession(ctx, "sess-1")

	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2", len(events))
	}
	for _, e := range events {
		if e.Type != schema.EventSkillDeactivate {
			t.Errorf("event type = %q, want %q", e.Type, schema.EventSkillDeactivate)
		}
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	reg := setupTestRegistry(t)
	mgr := NewManager(reg)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess := "sess-1"
			if i%2 == 0 {
				sess = "sess-2"
			}
			_, _ = mgr.Activate(ctx, "valid-skill", sess)
			mgr.ActiveSkills(sess)
			_ = mgr.Deactivate(ctx, "valid-skill", sess)
		}(i)
	}
	wg.Wait()
}
