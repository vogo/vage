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

package context_tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/skill"
	"github.com/vogo/vage/tool"
	"github.com/vogo/vage/vector"
	"github.com/vogo/vage/workspace"
)

func TestBuilder_UnifiedBudget_OldestDroppedAcrossSources(t *testing.T) {
	sess := memory.NewSessionMemory("ub", "ub-session")
	ctx := context.Background()
	body := strings.Repeat("x", 40) // ~10 tokens
	for i := range 4 {
		key := fmt.Sprintf("msg:%06d", i)
		text := fmt.Sprintf("%02d:%s", i, body)
		if err := sess.Set(ctx, key, schema.NewUserMessage(schema.ProtocolOpenAIChat, text), 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mm := memory.NewManager(memory.WithSession(sess))

	store := vector.NewMapVectorStore()
	emb := vector.NewHashEmbedder(32)
	doc := strings.Repeat("fox ", 20)
	vec, err := emb.Embed(ctx, doc)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := store.Add(ctx, vector.Document{ID: "d1", Text: doc, Embedding: vec}); err != nil {
		t.Fatalf("add: %v", err)
	}

	ws, err := workspace.NewFileWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	plan := strings.Repeat("H", 80) + strings.Repeat("T", 20)
	if err := ws.WritePlan(ctx, "ub-session", plan); err != nil {
		t.Fatalf("WritePlan: %v", err)
	}

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("S")}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: mm}),
		vctx.WithSource(&vctx.WorkspaceSource{Workspace: ws, MaxBytes: 20}),
		vctx.WithSource(&vctx.VectorRecallSource{Store: store, Embedder: emb, TopK: 1}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: "ub-session",
		AgentID:   "ub",
		Budget:    50,
		Intent:    "fox",
		Request: &schema.RunRequest{
			SessionID: "ub-session",
			Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "q")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if res.Report.OutputTokens > 50 {
		t.Errorf("OutputTokens=%d exceeds window 50", res.Report.OutputTokens)
	}

	dropped := 0
	var wsRep *schema.ContextSourceReport
	var memRep *schema.ContextSourceReport
	for i := range res.Report.Sources {
		s := &res.Report.Sources[i]
		dropped += s.DroppedN
		switch s.Source {
		case vctx.SourceNameWorkspace:
			wsRep = s
		case vctx.SourceNameSessionMemory:
			memRep = s
		}
	}
	if res.Report.DroppedCount != dropped {
		t.Errorf("DroppedCount=%d, sum of source DroppedN=%d", res.Report.DroppedCount, dropped)
	}
	if memRep == nil || memRep.OriginalCount != 4 {
		t.Fatalf("session report missing or OriginalCount=%v", memRep)
	}
	if wsRep == nil {
		t.Fatal("missing workspace report")
	}
	if wsRep.Status != vctx.StatusTruncated {
		t.Errorf("workspace status=%q, want truncated", wsRep.Status)
	}
	if !strings.Contains(wsRep.Note, vctx.NoteWorkspaceTailKeep) {
		t.Errorf("workspace note=%q missing %s", wsRep.Note, vctx.NoteWorkspaceTailKeep)
	}

	evt := res.Report.ToEventData()
	if evt.DroppedCount != res.Report.DroppedCount || evt.AvailableHistory != res.Report.AvailableHistory {
		t.Errorf("event payload diverged from report")
	}
	if len(evt.Sources) != len(res.Report.Sources) {
		t.Errorf("event sources %d vs report %d", len(evt.Sources), len(res.Report.Sources))
	}
}

func TestTaskAgent_WorkspaceTailKeep_StreamAndHookOnce(t *testing.T) {
	ctx := context.Background()
	ws, err := workspace.NewFileWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	plan := strings.Repeat("H", 90) + strings.Repeat("T", 10)
	if err := ws.WritePlan(ctx, "tail-session", plan); err != nil {
		t.Fatalf("WritePlan: %v", err)
	}

	rec := newRecordingHook()
	hm := installHook(rec)

	fake := &largemodel.FakeCaller{
		Chunks: []*largemodel.Chunk{
			{TextDelta: "ok", FinishReason: largemodel.FinishReasonStop},
		},
	}

	a := taskagent.New(
		agent.Config{ID: "tail-agent"},
		taskagent.WithCaller(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Sys.")),
		taskagent.WithHookManager(hm),
		taskagent.WithExtraSources(&vctx.WorkspaceSource{Workspace: ws, MaxBytes: 10}),
	)

	rs, err := a.RunStream(ctx, &schema.RunRequest{
		SessionID: "tail-session",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var stream []schema.Event
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		stream = append(stream, e)
	}

	if len(stream) < 2 {
		t.Fatalf("stream events=%d", len(stream))
	}
	if stream[0].Type != schema.EventAgentStart {
		t.Errorf("stream[0]=%q, want agent_start", stream[0].Type)
	}
	if stream[1].Type != schema.EventContextBuilt {
		t.Errorf("stream[1]=%q, want context_built", stream[1].Type)
	}

	data, ok := stream[1].Data.(schema.ContextBuiltData)
	if !ok {
		t.Fatalf("stream ContextBuilt type=%T", stream[1].Data)
	}
	var wsRep *schema.ContextSourceReport
	for i := range data.Sources {
		if data.Sources[i].Source == vctx.SourceNameWorkspace {
			wsRep = &data.Sources[i]
		}
	}
	if wsRep == nil {
		t.Fatal("stream event missing workspace source")
	}
	if wsRep.Status != vctx.StatusTruncated {
		t.Errorf("workspace status=%q", wsRep.Status)
	}
	if !strings.Contains(wsRep.Note, vctx.NoteWorkspaceTailKeep) {
		t.Errorf("note=%q", wsRep.Note)
	}

	hookBuilt := rec.byType(schema.EventContextBuilt)
	if len(hookBuilt) != 1 {
		t.Fatalf("hook EventContextBuilt count=%d, want 1 (no duplicate)", len(hookBuilt))
	}
	hookData, ok := hookBuilt[0].Data.(schema.ContextBuiltData)
	if !ok {
		t.Fatalf("hook data type=%T", hookBuilt[0].Data)
	}
	if hookData.DroppedCount != data.DroppedCount {
		t.Errorf("hook DroppedCount=%d stream=%d", hookData.DroppedCount, data.DroppedCount)
	}
}

func TestTaskAgent_SkillSnapshotChargedBeforeModel(t *testing.T) {
	ctx := context.Background()
	reg := skill.NewRegistry()
	_ = reg.Register(&skill.Def{
		Name:         "long-skill",
		Description:  "d",
		Instructions: strings.Repeat("SKILLINST ", 20),
		AllowedTools: []string{"allowed"},
	})
	sm := skill.NewManager(reg)
	if _, err := sm.Activate(ctx, "long-skill", "skill-session"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	tools := tool.NewRegistry()
	_ = tools.Register(schema.ToolDef{Name: "allowed", Description: "ok"}, func(context.Context, string, string) (schema.ToolResult, error) {
		return schema.TextResult("", "x"), nil
	})
	_ = tools.Register(schema.ToolDef{Name: "blocked", Description: "no"}, func(context.Context, string, string) (schema.ToolResult, error) {
		return schema.TextResult("", "y"), nil
	})

	sess := memory.NewSessionMemory("skill-agent", "skill-session")
	for i := range 6 {
		_ = sess.Set(ctx, fmt.Sprintf("msg:%06d", i),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, fmt.Sprintf("%02d:%s", i, strings.Repeat("h", 200))), 0)
	}
	mm := memory.NewManager(
		memory.WithSession(sess),
		memory.WithCompressor(memory.NewTokenBudgetCompressor()),
	)

	rec := newRecordingHook()
	fake := newFake(stopResponse("ok"))
	a := taskagent.New(
		agent.Config{ID: "skill-agent"},
		taskagent.WithCaller(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Base.")),
		taskagent.WithSkillManager(sm),
		taskagent.WithToolRegistry(tools),
		taskagent.WithMemory(mm),
		taskagent.WithHookManager(installHook(rec)),
		taskagent.WithContextBudget(memory.Budget{ModelContextTokens: 200}),
	)

	_, err := a.Run(ctx, &schema.RunRequest{
		SessionID: "skill-session",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "now")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.firstRequest(t)
	if !strings.Contains(req.Messages[0].Text(), "SKILLINST") {
		t.Fatalf("system missing skill text: %q", req.Messages[0].Text())
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "allowed" {
		t.Fatalf("tools=%v, want [allowed] from the same skill snapshot", req.Tools)
	}

	events := rec.byType(schema.EventContextBuilt)
	if len(events) != 1 {
		t.Fatalf("EventContextBuilt count=%d", len(events))
	}
	data := events[0].Data.(schema.ContextBuiltData)
	if data.ReservedSystem <= 0 {
		t.Errorf("ReservedSystem=%d, want skill+base charged", data.ReservedSystem)
	}
	if data.OutputTokens <= 0 {
		t.Errorf("OutputTokens=%d", data.OutputTokens)
	}

	var mem *schema.ContextSourceReport
	for i := range data.Sources {
		if data.Sources[i].Source == vctx.SourceNameSessionMemory {
			mem = &data.Sources[i]
		}
	}
	if mem == nil || mem.OriginalCount != 6 {
		t.Fatalf("session OriginalCount=%v", mem)
	}
	if mem.OutputN >= 6 {
		t.Errorf("expected history compression under shared budget, OutputN=%d AvailableHistory=%d",
			mem.OutputN, data.AvailableHistory)
	}
}

func TestTaskAgent_FixedContentOverflowFailsClosed(t *testing.T) {
	ctx := context.Background()
	reg := skill.NewRegistry()
	_ = reg.Register(&skill.Def{
		Name:         "huge-skill",
		Description:  "d",
		Instructions: strings.Repeat("X", 400),
	})
	sm := skill.NewManager(reg)
	_, _ = sm.Activate(ctx, "huge-skill", "ovf-session")

	fake := newFake(stopResponse("should not be called"))
	a := taskagent.New(
		agent.Config{ID: "ovf"},
		taskagent.WithCaller(fake),
		taskagent.WithSystemPrompt(prompt.StringPrompt("Base.")),
		taskagent.WithSkillManager(sm),
		taskagent.WithContextBudget(memory.Budget{ModelContextTokens: 20, ReservedOutput: 8}),
	)

	_, err := a.Run(ctx, &schema.RunRequest{
		SessionID: "ovf-session",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "q")},
	})
	if err == nil {
		t.Fatal("expected fail-closed budget error before model call")
	}
	if !errors.Is(err, vctx.ErrFixedContentExceedsBudget) && !strings.Contains(err.Error(), "exceeds model window") {
		t.Errorf("error = %v", err)
	}
	if len(fake.Requests()) != 0 {
		t.Errorf("model was called %d times; want 0", len(fake.Requests()))
	}
}
