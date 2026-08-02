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

package skill_tests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/service"
	"github.com/vogo/vage/skill"
	"github.com/vogo/vage/tool"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createTestSkillDir creates a temp directory with skill fixtures and returns
// its path. The caller should use t.Cleanup to remove it.
func createTestSkillDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Skill: pdf-processing (has allowed tools and a resource)
	pdfDir := filepath.Join(dir, "pdf-processing")
	must(t, os.MkdirAll(filepath.Join(pdfDir, "scripts"), 0o755))
	must(t, os.MkdirAll(filepath.Join(pdfDir, "references"), 0o755))

	writeFile(t, filepath.Join(pdfDir, "SKILL.md"), `---
name: pdf-processing
description: Process PDF documents
license: Apache-2.0
allowed_tools:
  - pdf-reader
  - pdf-writer
metadata:
  version: "2.0"
---

You are a PDF processing assistant.

Steps:
1. Read the PDF
2. Extract text
3. Return summary
`)

	writeFile(t, filepath.Join(pdfDir, "scripts", "convert.sh"), "#!/bin/bash\necho converting\n")
	writeFile(t, filepath.Join(pdfDir, "references", "format-spec.md"), "# PDF Format Spec\n\nDetails here.\n")

	// Skill: text-analysis (no allowed tools, no resources)
	textDir := filepath.Join(dir, "text-analysis")
	must(t, os.MkdirAll(textDir, 0o755))

	writeFile(t, filepath.Join(textDir, "SKILL.md"), `---
name: text-analysis
description: Analyze text content for sentiment and entities
---

You are a text analysis assistant. Identify sentiment and named entities.
`)

	// Non-skill directory (no SKILL.md) -- should be skipped during discovery.
	must(t, os.MkdirAll(filepath.Join(dir, "random-dir"), 0o755))
	writeFile(t, filepath.Join(dir, "random-dir", "README.md"), "not a skill\n")

	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// capturingChatCompleter records ChatRequests sent to it so tests can inspect
// the system prompt and tools list.
type capturingChatCompleter struct {
	mu       sync.Mutex
	requests []*largemodel.Request
}

func (c *capturingChatCompleter) ChatCompletion(_ context.Context, req *largemodel.Request) (*largemodel.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()

	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "ok"),
			FinishReason: largemodel.FinishReasonStop,
		}},
		Usage: schema.Usage{TotalTokens: 10},
	}, nil
}

func (c *capturingChatCompleter) ChatCompletionStream(_ context.Context, _ *largemodel.Request) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented")
}

func (c *capturingChatCompleter) getRequests() []*largemodel.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]*largemodel.Request, len(c.requests))
	copy(cp, c.requests)
	return cp
}

// ---------------------------------------------------------------------------
// Test 1: Full Lifecycle (no LLM required)
//
// Covers the entire skill lifecycle end-to-end:
//   - Discover skills from a temp directory using FileLoader
//   - Register all discovered skills via Registry
//   - Activate a skill for a session
//   - Verify ActiveSkills returns the activation
//   - Load a resource and verify content is read from disk
//   - Deactivate the skill
//   - Verify ActiveSkills returns empty
//   - Verify deactivating an already-deactivated skill returns error
//   - Verify loading resource for inactive skill returns error
//   - Verify ClearSession removes all activations
//
// ---------------------------------------------------------------------------
func TestSkillFullLifecycle(t *testing.T) {
	dir := createTestSkillDir(t)
	ctx := context.Background()

	// Step 1: Discover skills.
	loader := &skill.FileLoader{}
	skills, err := loader.Discover(ctx, dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Step 2: Register all discovered skills.
	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	for _, def := range skills {
		if err := registry.Register(def); err != nil {
			t.Fatalf("Register %q: %v", def.Name, err)
		}
	}

	// Verify registry contents.
	if _, ok := registry.Get("pdf-processing"); !ok {
		t.Error("pdf-processing not found in registry")
	}
	if _, ok := registry.Get("text-analysis"); !ok {
		t.Error("text-analysis not found in registry")
	}
	if len(registry.List()) != 2 {
		t.Errorf("List() returned %d, want 2", len(registry.List()))
	}

	// Step 3: Create manager and activate a skill.
	manager := skill.NewManager(registry)
	sessionID := "session-001"

	activation, err := manager.Activate(ctx, "pdf-processing", sessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activation.SkillName != "pdf-processing" {
		t.Errorf("SkillName = %q, want %q", activation.SkillName, "pdf-processing")
	}
	if activation.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", activation.SessionID, sessionID)
	}
	if activation.ActivatedAt.IsZero() {
		t.Error("ActivatedAt is zero")
	}
	def := activation.SkillDef()
	if def.Name == "" {
		t.Fatal("SkillDef is empty")
	}

	// Step 4: Verify ActiveSkills returns the activation.
	active := manager.ActiveSkills(sessionID)
	if len(active) != 1 {
		t.Fatalf("ActiveSkills: got %d, want 1", len(active))
	}
	if active[0].SkillName != "pdf-processing" {
		t.Errorf("ActiveSkills[0].SkillName = %q, want %q", active[0].SkillName, "pdf-processing")
	}

	// Step 5: Load a resource and verify content.
	res, err := manager.LoadResource(ctx, sessionID, "pdf-processing", skill.ResourceTypeScript, "convert.sh")
	if err != nil {
		t.Fatalf("LoadResource: %v", err)
	}
	if res.Type != skill.ResourceTypeScript {
		t.Errorf("resource Type = %q, want %q", res.Type, skill.ResourceTypeScript)
	}
	if res.Name != "convert.sh" {
		t.Errorf("resource Name = %q, want %q", res.Name, "convert.sh")
	}
	if !strings.Contains(res.Content, "echo converting") {
		t.Errorf("resource Content missing expected text, got: %q", res.Content)
	}

	// Load a reference resource too.
	refRes, err := manager.LoadResource(ctx, sessionID, "pdf-processing", skill.ResourceTypeReference, "format-spec.md")
	if err != nil {
		t.Fatalf("LoadResource (reference): %v", err)
	}
	if !strings.Contains(refRes.Content, "PDF Format Spec") {
		t.Errorf("reference Content missing expected text, got: %q", refRes.Content)
	}

	// Load the same resource again to verify consistency.
	res2, err := manager.LoadResource(ctx, sessionID, "pdf-processing", skill.ResourceTypeScript, "convert.sh")
	if err != nil {
		t.Fatalf("LoadResource (second): %v", err)
	}
	if res2.Content != res.Content {
		t.Error("second load content differs from original")
	}

	// Step 6: Deactivate the skill.
	if err := manager.Deactivate(ctx, "pdf-processing", sessionID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	// Step 7: Verify ActiveSkills returns empty.
	active = manager.ActiveSkills(sessionID)
	if len(active) != 0 {
		t.Errorf("ActiveSkills after deactivate: got %d, want 0", len(active))
	}

	// Deactivating again should return an error.
	if err := manager.Deactivate(ctx, "pdf-processing", sessionID); err == nil {
		t.Error("expected error deactivating inactive skill, got nil")
	}

	// Loading resource for deactivated skill should fail.
	_, err = manager.LoadResource(ctx, sessionID, "pdf-processing", skill.ResourceTypeScript, "convert.sh")
	if err == nil {
		t.Error("expected error loading resource for inactive skill, got nil")
	}

	// Step 8: Test ClearSession.
	_, _ = manager.Activate(ctx, "pdf-processing", sessionID)
	_, _ = manager.Activate(ctx, "text-analysis", sessionID)
	if active := manager.ActiveSkills(sessionID); len(active) != 2 {
		t.Fatalf("ActiveSkills before clear = %d, want 2", len(active))
	}
	manager.ClearSession(ctx, sessionID)
	if active := manager.ActiveSkills(sessionID); len(active) != 0 {
		t.Errorf("ActiveSkills after ClearSession = %d, want 0", len(active))
	}
}

// ---------------------------------------------------------------------------
// Test 2: TaskAgent with Skills (mock ChatCompleter)
//
// Verifies TaskAgent integration:
//   - Skill instructions are injected into the system prompt
//   - Skill AllowedTools filter the tools sent to the LLM
//   - Multiple active skills have their instructions merged
//   - Tool intersection with request-level filter works
//
// ---------------------------------------------------------------------------
func TestSkillTaskAgentIntegration(t *testing.T) {
	ctx := context.Background()

	// Set up skills.
	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	must(t, registry.Register(&skill.Def{
		Name:         "pdf-processing",
		Description:  "Process PDFs",
		Instructions: "You are a PDF assistant.",
		AllowedTools: []string{"pdf-reader", "pdf-writer"},
	}))
	must(t, registry.Register(&skill.Def{
		Name:         "text-analysis",
		Description:  "Analyze text",
		Instructions: "You are a text analysis assistant.",
	}))

	manager := skill.NewManager(registry)
	sessionID := "session-llm"

	_, err := manager.Activate(ctx, "pdf-processing", sessionID)
	must(t, err)

	// Register tools -- more than the skill allows.
	toolReg := tool.NewRegistry()
	must(t, toolReg.Register(schema.ToolDef{Name: "pdf-reader", Description: "Read PDFs"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "pdf-writer", Description: "Write PDFs"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "calculator", Description: "Math"}, noopHandler))

	cc := &capturingChatCompleter{}

	a := taskagent.New(agent.Config{ID: "skill-test-agent"},
		taskagent.WithCaller(cc),
		taskagent.WithToolRegistry(toolReg),
		taskagent.WithSkillManager(manager),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Base system prompt.")),
	)

	resp, err := a.Run(ctx, &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "Process this PDF")},
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	// Verify the system prompt contains skill instructions.
	reqs := cc.getRequests()
	if len(reqs) == 0 {
		t.Fatal("no ChatRequests captured")
	}

	sysPrompt := ""
	for _, msg := range reqs[0].Messages {
		if msg.Role() == schema.RoleSystem {
			sysPrompt = msg.Text()
			break
		}
	}

	if !strings.Contains(sysPrompt, "Base system prompt.") {
		t.Error("system prompt missing base prompt text")
	}
	if !strings.Contains(sysPrompt, `<skill name="pdf-processing">`) {
		t.Error("system prompt missing skill tag for pdf-processing")
	}
	if !strings.Contains(sysPrompt, "You are a PDF assistant.") {
		t.Error("system prompt missing pdf-processing instructions")
	}

	// Verify tool filtering: only pdf-reader and pdf-writer should be sent.
	tools := reqs[0].Tools
	toolNames := make(map[string]bool, len(tools))
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}
	if !toolNames["pdf-reader"] {
		t.Error("pdf-reader tool missing from LLM request")
	}
	if !toolNames["pdf-writer"] {
		t.Error("pdf-writer tool missing from LLM request")
	}
	if toolNames["calculator"] {
		t.Error("calculator tool should have been filtered out by skill AllowedTools")
	}
}

// TestSkillTaskAgentMultipleSkills verifies that multiple active skills merge
// their instructions and tool allowlists.
//
//   - Two skills activated for the same session
//   - Both skill instructions appear in the system prompt
//   - When all skills declare AllowedTools, union is used; when any skill is
//     unrestricted (no AllowedTools), all tools pass through.
func TestSkillTaskAgentMultipleSkills(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	must(t, registry.Register(&skill.Def{
		Name:         "skill-a",
		Description:  "Skill A",
		Instructions: "Instruction A content.",
		AllowedTools: []string{"tool-x", "tool-y"},
	}))
	must(t, registry.Register(&skill.Def{
		Name:         "skill-b",
		Description:  "Skill B",
		Instructions: "Instruction B content.",
		AllowedTools: []string{"tool-y", "tool-z"},
	}))

	manager := skill.NewManager(registry)
	sessionID := "session-multi"
	_, err := manager.Activate(ctx, "skill-a", sessionID)
	must(t, err)
	_, err = manager.Activate(ctx, "skill-b", sessionID)
	must(t, err)

	// Register all tools.
	toolReg := tool.NewRegistry()
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-x", Description: "X"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-y", Description: "Y"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-z", Description: "Z"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-w", Description: "W"}, noopHandler))

	cc := &capturingChatCompleter{}
	a := taskagent.New(agent.Config{ID: "multi-skill-agent"},
		taskagent.WithCaller(cc),
		taskagent.WithToolRegistry(toolReg),
		taskagent.WithSkillManager(manager),
	)

	_, err = a.Run(ctx, &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
		SessionID: sessionID,
	})
	must(t, err)

	reqs := cc.getRequests()
	if len(reqs) == 0 {
		t.Fatal("no requests")
	}

	// Both instructions should appear in system prompt.
	sysPrompt := ""
	for _, msg := range reqs[0].Messages {
		if msg.Role() == schema.RoleSystem {
			sysPrompt = msg.Text()
			break
		}
	}
	if !strings.Contains(sysPrompt, "Instruction A content.") {
		t.Error("system prompt missing skill-a instructions")
	}
	if !strings.Contains(sysPrompt, "Instruction B content.") {
		t.Error("system prompt missing skill-b instructions")
	}

	// Tool union: tool-x, tool-y, tool-z should be present; tool-w should not.
	tools := reqs[0].Tools
	toolNames := make(map[string]bool, len(tools))
	for _, tl := range tools {
		toolNames[tl.Function.Name] = true
	}
	if !toolNames["tool-x"] || !toolNames["tool-y"] || !toolNames["tool-z"] {
		t.Errorf("expected tool-x, tool-y, tool-z in tools, got: %v", toolNames)
	}
	if toolNames["tool-w"] {
		t.Error("tool-w should be filtered out")
	}
}

// TestSkillNoAllowedToolsPassesAllTools verifies that when no active skill
// declares AllowedTools, all tools are passed through (existing behavior).
func TestSkillNoAllowedToolsPassesAllTools(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	must(t, registry.Register(&skill.Def{
		Name:         "no-tools-skill",
		Description:  "No tools",
		Instructions: "Instruction with no tool filter.",
	}))

	manager := skill.NewManager(registry)
	sessionID := "session-no-tools"
	_, err := manager.Activate(ctx, "no-tools-skill", sessionID)
	must(t, err)

	toolReg := tool.NewRegistry()
	must(t, toolReg.Register(schema.ToolDef{Name: "any-tool", Description: "Any"}, noopHandler))

	cc := &capturingChatCompleter{}
	a := taskagent.New(agent.Config{ID: "no-tools-agent"},
		taskagent.WithCaller(cc),
		taskagent.WithToolRegistry(toolReg),
		taskagent.WithSkillManager(manager),
	)

	_, err = a.Run(ctx, &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		SessionID: sessionID,
	})
	must(t, err)

	reqs := cc.getRequests()
	if len(reqs) == 0 {
		t.Fatal("no requests")
	}
	if len(reqs[0].Tools) != 1 {
		t.Errorf("expected 1 tool (all pass through), got %d", len(reqs[0].Tools))
	}
}

// TestSkillMixedAllowedToolsPassesAll verifies that when one active skill
// declares AllowedTools but another does not (unrestricted), all tools pass through.
func TestSkillMixedAllowedToolsPassesAll(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	must(t, registry.Register(&skill.Def{
		Name:         "restricted-skill",
		Description:  "Has allowed tools",
		Instructions: "Restricted.",
		AllowedTools: []string{"tool-a"},
	}))
	must(t, registry.Register(&skill.Def{
		Name:         "unrestricted-skill",
		Description:  "No allowed tools",
		Instructions: "Unrestricted.",
	}))

	manager := skill.NewManager(registry)
	sessionID := "session-mixed"
	_, _ = manager.Activate(ctx, "restricted-skill", sessionID)
	_, _ = manager.Activate(ctx, "unrestricted-skill", sessionID)

	toolReg := tool.NewRegistry()
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-a", Description: "A"}, noopHandler))
	must(t, toolReg.Register(schema.ToolDef{Name: "tool-b", Description: "B"}, noopHandler))

	cc := &capturingChatCompleter{}
	a := taskagent.New(agent.Config{ID: "mixed-agent"},
		taskagent.WithCaller(cc),
		taskagent.WithToolRegistry(toolReg),
		taskagent.WithSkillManager(manager),
	)

	_, err := a.Run(ctx, &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")},
		SessionID: sessionID,
	})
	must(t, err)

	reqs := cc.getRequests()
	if len(reqs) == 0 {
		t.Fatal("no requests")
	}
	// Both tools should pass through because unrestricted-skill has no AllowedTools.
	if len(reqs[0].Tools) != 2 {
		t.Errorf("expected 2 tools (unrestricted skill), got %d", len(reqs[0].Tools))
	}
}

func noopHandler(_ context.Context, _, _ string) (schema.ToolResult, error) {
	return schema.TextResult("", "ok"), nil
}

// ---------------------------------------------------------------------------
// Test 3: Hook Event Observation
//
// Verifies that the Manager dispatches the correct events during
// skill lifecycle operations:
//   - EventSkillActivate on Activate
//   - EventSkillResourceLoad on LoadResource
//   - EventSkillDeactivate on Deactivate
//
// ---------------------------------------------------------------------------
func TestSkillHookEvents(t *testing.T) {
	dir := createTestSkillDir(t)
	ctx := context.Background()

	loader := &skill.FileLoader{}
	skills, err := loader.Discover(ctx, dir)
	must(t, err)

	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	for _, def := range skills {
		must(t, registry.Register(def))
	}

	// Collect events via the EventDispatcher callback.
	var mu sync.Mutex
	var events []schema.Event

	dispatcher := func(_ context.Context, e schema.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}

	manager := skill.NewManager(registry, skill.WithEventDispatcher(dispatcher))
	sessionID := "session-events"

	// Activate.
	_, err = manager.Activate(ctx, "pdf-processing", sessionID)
	must(t, err)

	// Load a resource.
	_, err = manager.LoadResource(ctx, sessionID, "pdf-processing", skill.ResourceTypeScript, "convert.sh")
	must(t, err)

	// Deactivate.
	must(t, manager.Deactivate(ctx, "pdf-processing", sessionID))

	// Verify events.
	mu.Lock()
	captured := make([]schema.Event, len(events))
	copy(captured, events)
	mu.Unlock()

	if len(captured) != 3 {
		t.Fatalf("expected 3 events, got %d", len(captured))
	}

	// Event 1: skill_activate
	if captured[0].Type != schema.EventSkillActivate {
		t.Errorf("event[0].Type = %q, want %q", captured[0].Type, schema.EventSkillActivate)
	}
	activateData, ok := captured[0].Data.(schema.SkillActivateData)
	if !ok {
		t.Fatalf("event[0].Data type = %T, want SkillActivateData", captured[0].Data)
	}
	if activateData.SkillName != "pdf-processing" {
		t.Errorf("activate event SkillName = %q, want %q", activateData.SkillName, "pdf-processing")
	}
	if activateData.SessionID != sessionID {
		t.Errorf("activate event SessionID = %q, want %q", activateData.SessionID, sessionID)
	}

	// Event 2: skill_resource_load
	if captured[1].Type != schema.EventSkillResourceLoad {
		t.Errorf("event[1].Type = %q, want %q", captured[1].Type, schema.EventSkillResourceLoad)
	}
	loadData, ok := captured[1].Data.(schema.SkillResourceLoadData)
	if !ok {
		t.Fatalf("event[1].Data type = %T, want SkillResourceLoadData", captured[1].Data)
	}
	if loadData.SkillName != "pdf-processing" {
		t.Errorf("load event SkillName = %q, want %q", loadData.SkillName, "pdf-processing")
	}
	if loadData.ResourceType != skill.ResourceTypeScript {
		t.Errorf("load event ResourceType = %q, want %q", loadData.ResourceType, skill.ResourceTypeScript)
	}
	if loadData.ResourceName != "convert.sh" {
		t.Errorf("load event ResourceName = %q, want %q", loadData.ResourceName, "convert.sh")
	}

	// Event 3: skill_deactivate
	if captured[2].Type != schema.EventSkillDeactivate {
		t.Errorf("event[2].Type = %q, want %q", captured[2].Type, schema.EventSkillDeactivate)
	}
	deactData, ok := captured[2].Data.(schema.SkillDeactivateData)
	if !ok {
		t.Fatalf("event[2].Data type = %T, want SkillDeactivateData", captured[2].Data)
	}
	if deactData.SkillName != "pdf-processing" {
		t.Errorf("deactivate event SkillName = %q", deactData.SkillName)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Service Startup Discovery
//
// Verifies that Service.Start discovers and registers skills from a directory:
//   - Skills are discovered and registered into the service's Manager
//   - Manager is created automatically when WithSkillDir is set
//   - Non-skill directories are ignored
//
// ---------------------------------------------------------------------------
func TestSkillServiceDiscovery(t *testing.T) {
	dir := createTestSkillDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := service.New(service.Config{Addr: ":0"}, service.WithSkillDir(dir))

	// Start in background.
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Start(ctx) }()

	// Wait until the server is listening (discoverSkills runs before Serve).
	for range 100 {
		if svc.ListenAddr() != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if svc.ListenAddr() == "" {
		cancel()
		t.Fatal("server did not start within timeout")
	}

	// Verify the Manager was created and skills registered.
	sm := svc.SkillManager()
	if sm == nil {
		cancel()
		t.Fatal("Manager is nil after Start with skill dir")
	}

	// The manager should be able to activate the discovered skills.
	act, err := sm.Activate(ctx, "pdf-processing", "test-session")
	if err != nil {
		cancel()
		t.Fatalf("Activate pdf-processing: %v", err)
	}
	actDef := act.SkillDef()
	if actDef.Description != "Process PDF documents" {
		t.Errorf("description = %q, want %q", actDef.Description, "Process PDF documents")
	}

	act2, err := sm.Activate(ctx, "text-analysis", "test-session")
	if err != nil {
		cancel()
		t.Fatalf("Activate text-analysis: %v", err)
	}
	act2Def := act2.SkillDef()
	if act2Def.Description != "Analyze text content for sentiment and entities" {
		t.Errorf("description = %q, want %q", act2Def.Description, "Analyze text content for sentiment and entities")
	}

	// Activating a non-existent skill should fail.
	_, err = sm.Activate(ctx, "nonexistent", "test-session")
	if err == nil {
		t.Error("expected error activating non-existent skill")
	}

	cancel()
}

// ---------------------------------------------------------------------------
// Test 5: Concurrent Access
//
// Exercises the skill system from multiple goroutines simultaneously:
//   - Concurrent activate/deactivate/query operations
//   - Run with -race flag to detect data races
//
// ---------------------------------------------------------------------------
func TestSkillConcurrentAccess(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry(skill.WithValidator(skill.DefaultValidator()))
	names := []string{
		"skill-a", "skill-b", "skill-c", "skill-d", "skill-e",
		"skill-f", "skill-g", "skill-h", "skill-i", "skill-j",
	}
	for _, name := range names {
		must(t, registry.Register(&skill.Def{
			Name:         name,
			Description:  "Skill " + name,
			Instructions: "Instructions for " + name,
		}))
	}

	manager := skill.NewManager(registry)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			sessionID := "session-concurrent"

			// Each goroutine activates and deactivates a unique skill
			// to avoid duplicate activation errors.
			skillName := names[id%len(names)]
			mySession := sessionID + "-" + skillName + "-" + string(rune('0'+id))

			_, _ = manager.Activate(ctx, skillName, mySession)
			_ = manager.ActiveSkills(mySession)
			_ = manager.Deactivate(ctx, skillName, mySession)
			_ = manager.ActiveSkills(mySession)
		}(g)
	}

	wg.Wait()

	// Also test concurrent registry operations.
	var wg2 sync.WaitGroup
	wg2.Add(goroutines)

	for g := range goroutines {
		go func(id int) {
			defer wg2.Done()
			// Read operations.
			_ = registry.List()
			_ = registry.Match("skill")
			_, _ = registry.Get("skill-a")
		}(g)
	}

	wg2.Wait()
}

// ---------------------------------------------------------------------------
// Test: Duplicate activation prevention
//
// Verifies that activating the same skill twice for the same session fails.
// ---------------------------------------------------------------------------
func TestSkillDuplicateActivation(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:         "test-skill",
		Description:  "Test",
		Instructions: "Do stuff.",
	}))

	manager := skill.NewManager(registry)
	sessionID := "session-dup"

	_, err := manager.Activate(ctx, "test-skill", sessionID)
	must(t, err)

	_, err = manager.Activate(ctx, "test-skill", sessionID)
	if err == nil {
		t.Error("expected error on duplicate activation, got nil")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Errorf("error message = %q, want it to contain 'already active'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Test: Session isolation
//
// Verifies that skill activations in one session do not affect another session.
// ---------------------------------------------------------------------------
func TestSkillSessionIsolation(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:         "shared-skill",
		Description:  "Shared",
		Instructions: "Shared instructions.",
	}))

	manager := skill.NewManager(registry)

	// Activate for session A.
	_, err := manager.Activate(ctx, "shared-skill", "session-A")
	must(t, err)

	// Session B should have no active skills.
	if active := manager.ActiveSkills("session-B"); len(active) != 0 {
		t.Errorf("session-B has %d active skills, want 0", len(active))
	}

	// Activate for session B too.
	_, err = manager.Activate(ctx, "shared-skill", "session-B")
	must(t, err)

	// Both sessions should have 1 active skill.
	if active := manager.ActiveSkills("session-A"); len(active) != 1 {
		t.Errorf("session-A has %d active skills, want 1", len(active))
	}
	if active := manager.ActiveSkills("session-B"); len(active) != 1 {
		t.Errorf("session-B has %d active skills, want 1", len(active))
	}

	// Deactivate from session A; session B should not be affected.
	must(t, manager.Deactivate(ctx, "shared-skill", "session-A"))
	if active := manager.ActiveSkills("session-A"); len(active) != 0 {
		t.Errorf("session-A has %d active skills after deactivate, want 0", len(active))
	}
	if active := manager.ActiveSkills("session-B"); len(active) != 1 {
		t.Errorf("session-B has %d active skills, want 1", len(active))
	}
}

// ---------------------------------------------------------------------------
// Test: Activate non-existent skill
//
// Verifies that activating a skill not in the registry returns an error.
// ---------------------------------------------------------------------------
func TestSkillActivateNonExistent(t *testing.T) {
	ctx := context.Background()
	registry := skill.NewRegistry()
	manager := skill.NewManager(registry)

	_, err := manager.Activate(ctx, "does-not-exist", "session-x")
	if err == nil {
		t.Error("expected error activating non-existent skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain 'not found'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Test: Load non-existent resource
//
// Verifies that loading a resource that does not exist returns an error.
// ---------------------------------------------------------------------------
func TestSkillLoadNonExistentResource(t *testing.T) {
	ctx := context.Background()

	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:         "empty-skill",
		Description:  "Empty",
		Instructions: "Nothing here.",
	}))

	manager := skill.NewManager(registry)
	_, err := manager.Activate(ctx, "empty-skill", "session-res")
	must(t, err)

	_, err = manager.LoadResource(ctx, "session-res", "empty-skill", skill.ResourceTypeScript, "nonexistent.sh")
	if err == nil {
		t.Error("expected error loading non-existent resource")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain 'not found'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Test: Registry Match
//
// Verifies that the registry Match function works with AND semantics.
// ---------------------------------------------------------------------------
func TestSkillRegistryMatch(t *testing.T) {
	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:        "pdf-reader",
		Description: "Reads PDF documents and extracts text",
	}))
	must(t, registry.Register(&skill.Def{
		Name:        "pdf-writer",
		Description: "Creates PDF documents from text",
	}))
	must(t, registry.Register(&skill.Def{
		Name:        "text-search",
		Description: "Searches text content",
	}))

	// Single word match.
	results := registry.Match("pdf")
	if len(results) != 2 {
		t.Errorf("Match('pdf') returned %d results, want 2", len(results))
	}

	// Multi-word AND match.
	results = registry.Match("pdf text")
	if len(results) != 2 {
		// Both pdf-reader ("extracts text") and pdf-writer ("from text") contain both words.
		t.Errorf("Match('pdf text') returned %d results, want 2", len(results))
	}

	// Query that matches nothing.
	results = registry.Match("nonexistent-xyz")
	if len(results) != 0 {
		t.Errorf("Match('nonexistent-xyz') returned %d results, want 0", len(results))
	}

	// Empty query returns all.
	results = registry.Match("")
	if len(results) != 3 {
		t.Errorf("Match('') returned %d results, want 3", len(results))
	}
}

// ---------------------------------------------------------------------------
// Test: Duplicate registration
//
// Verifies that registering a skill with the same name twice fails.
// ---------------------------------------------------------------------------
func TestSkillDuplicateRegistration(t *testing.T) {
	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:        "dup-skill",
		Description: "First",
	}))

	err := registry.Register(&skill.Def{
		Name:        "dup-skill",
		Description: "Second",
	})
	if err == nil {
		t.Error("expected error on duplicate registration")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error = %q, want 'already registered'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Test: Unregister and re-register
//
// Verifies Unregister removes a skill and allows re-registration.
// ---------------------------------------------------------------------------
func TestSkillUnregisterAndReregister(t *testing.T) {
	registry := skill.NewRegistry()
	must(t, registry.Register(&skill.Def{
		Name:        "temp-skill",
		Description: "Temporary",
	}))

	registry.Unregister("temp-skill")
	if _, ok := registry.Get("temp-skill"); ok {
		t.Error("skill should be removed after Unregister")
	}

	// Re-register should succeed.
	must(t, registry.Register(&skill.Def{
		Name:        "temp-skill",
		Description: "Re-registered",
	}))

	def, ok := registry.Get("temp-skill")
	if !ok {
		t.Error("skill should exist after re-registration")
	}
	if def.Description != "Re-registered" {
		t.Errorf("Description = %q, want %q", def.Description, "Re-registered")
	}

	// Unregister non-existent: should not panic.
	registry.Unregister("nonexistent")
}
