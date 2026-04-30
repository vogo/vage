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

package tree

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// stubChatCompleter is a minimal ChatCompleter that records the incoming
// request and returns a hard-coded response. The tests do not exercise the
// streaming path; ChatCompletionStream returns ErrNotImplemented to make
// any accidental use noisy.
type stubChatCompleter struct {
	gotReq      *aimodel.ChatRequest
	respText    string
	respErr     error
	streamCalls int
}

func (s *stubChatCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	s.gotReq = req
	if s.respErr != nil {
		return nil, s.respErr
	}
	return &aimodel.ChatResponse{Choices: []aimodel.Choice{{
		Message: aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(s.respText)},
	}}}, nil
}

func (s *stubChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	s.streamCalls++
	return nil, errors.New("not implemented")
}

func TestNoopPromoter(t *testing.T) {
	p := NoopPromoter{}
	parent := &TreeNode{Title: "P", Summary: "S"}
	out, err := p.Summarize(context.Background(), parent, []*TreeNode{{Title: "C"}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if out != "S" {
		t.Errorf("got %q want %q", out, "S")
	}
	// nil parent must not panic.
	out, err = p.Summarize(context.Background(), nil, nil)
	if err != nil || out != "" {
		t.Errorf("nil parent: out=%q err=%v", out, err)
	}
}

func TestLLMPromoter_HappyPath(t *testing.T) {
	cli := &stubChatCompleter{respText: "  rolled-up paragraph  "}
	p := &LLMPromoter{Client: cli, Model: "test-model"}
	parent := &TreeNode{Title: "Build OAuth", Summary: "wiring deps", Status: StatusActive}
	children := []*TreeNode{
		{Type: NodeSubtask, Status: StatusDone, Title: "design schema", Summary: "foo+bar tables"},
		{Type: NodeFact, Status: StatusDone, Title: "callback wired", Summary: ""},
	}

	out, err := p.Summarize(context.Background(), parent, children)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if out != "rolled-up paragraph" {
		t.Errorf("output not trimmed: %q", out)
	}
	if cli.gotReq == nil || cli.gotReq.Model != "test-model" {
		t.Errorf("model not propagated: %+v", cli.gotReq)
	}
	if len(cli.gotReq.Messages) != 2 {
		t.Fatalf("messages=%d want 2", len(cli.gotReq.Messages))
	}
	userBody := cli.gotReq.Messages[1].Content.Text()
	if !strings.Contains(userBody, "Build OAuth") || !strings.Contains(userBody, "design schema") {
		t.Errorf("user body missing parent/child: %q", userBody)
	}
	if cli.gotReq.MaxTokens == nil || *cli.gotReq.MaxTokens != defaultLLMPromoterMaxTokens {
		t.Errorf("MaxTokens not set to default: %v", cli.gotReq.MaxTokens)
	}
}

func TestLLMPromoter_NoChildren(t *testing.T) {
	cli := &stubChatCompleter{respText: "should not be called"}
	p := &LLMPromoter{Client: cli}
	parent := &TreeNode{Summary: "current"}
	out, err := p.Summarize(context.Background(), parent, nil)
	if err != nil || out != "current" {
		t.Errorf("got out=%q err=%v", out, err)
	}
	if cli.gotReq != nil {
		t.Error("Client should not have been called")
	}
}

func TestLLMPromoter_NilClient(t *testing.T) {
	p := &LLMPromoter{}
	_, err := p.Summarize(context.Background(), &TreeNode{Title: "P"}, []*TreeNode{{Title: "C"}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("got %v want ErrInvalidArgument", err)
	}
}

func TestLLMPromoter_ChatError(t *testing.T) {
	wantErr := errors.New("network down")
	cli := &stubChatCompleter{respErr: wantErr}
	p := &LLMPromoter{Client: cli}
	_, err := p.Summarize(context.Background(), &TreeNode{Title: "P"}, []*TreeNode{{Title: "C"}})
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v want chain to wantErr", err)
	}
}

// fakeCompressor concatenates all input messages with a marker prefix so
// tests can verify the compressor was invoked with the expected payload.
type fakeCompressor struct {
	gotMsgs []schema.Message
	gotMax  int
	respErr error
}

func (f *fakeCompressor) Compress(_ context.Context, msgs []schema.Message, maxTokens int) ([]schema.Message, error) {
	f.gotMsgs = msgs
	f.gotMax = maxTokens
	if f.respErr != nil {
		return nil, f.respErr
	}
	out := []schema.Message{{Message: aimodel.Message{
		Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("compressed:" + msgs[0].Content.Text()),
	}}}
	return out, nil
}

func TestCompressorPromoter_HappyPath(t *testing.T) {
	c := &fakeCompressor{}
	p := &CompressorPromoter{Compressor: c}
	parent := &TreeNode{Title: "P", Summary: "old"}
	children := []*TreeNode{{Title: "child1", Status: StatusDone}}

	out, err := p.Summarize(context.Background(), parent, children)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.HasPrefix(out, "compressed:") {
		t.Errorf("output unexpected: %q", out)
	}
	if c.gotMax <= 0 {
		t.Errorf("token budget not propagated: %d", c.gotMax)
	}
	if len(c.gotMsgs) != 2 {
		t.Errorf("messages=%d want 2 (parent header + 1 child)", len(c.gotMsgs))
	}
}

func TestCompressorPromoter_NilCompressor(t *testing.T) {
	p := &CompressorPromoter{}
	_, err := p.Summarize(context.Background(), &TreeNode{Title: "P"}, []*TreeNode{{Title: "C"}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("got %v want ErrInvalidArgument", err)
	}
}

func TestCompressorPromoter_NoChildren(t *testing.T) {
	c := &fakeCompressor{}
	p := &CompressorPromoter{Compressor: c}
	out, err := p.Summarize(context.Background(), &TreeNode{Summary: "stay"}, nil)
	if err != nil || out != "stay" {
		t.Errorf("got %q err=%v", out, err)
	}
	if c.gotMsgs != nil {
		t.Error("Compressor should not have been invoked")
	}
}

func TestCompressorPromoter_Error(t *testing.T) {
	c := &fakeCompressor{respErr: errors.New("budget impossible")}
	p := &CompressorPromoter{Compressor: c}
	_, err := p.Summarize(context.Background(), &TreeNode{Title: "P"}, []*TreeNode{{Title: "C"}})
	if err == nil || !strings.Contains(err.Error(), "budget impossible") {
		t.Errorf("got %v want chain", err)
	}
}

// TestCompressorPromoter_RealCompressor uses memory.NewSlidingWindowCompressor
// so the test exercises a true ContextCompressor implementation rather than
// a fake. The output is non-empty because the sliding window keeps every
// message when the budget allows.
func TestCompressorPromoter_RealCompressor(t *testing.T) {
	cmp := memory.NewSlidingWindowCompressor(10)
	p := &CompressorPromoter{Compressor: cmp, MaxBytes: SummaryMaxBytes}

	parent := &TreeNode{Title: "Parent"}
	children := []*TreeNode{
		{Title: "c1", Type: NodeSubtask, Status: StatusDone},
		{Title: "c2", Type: NodeFact, Status: StatusDone, Summary: "fact body"},
	}
	out, err := p.Summarize(context.Background(), parent, children)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(out, "c1") || !strings.Contains(out, "c2") {
		t.Errorf("output missing children: %q", out)
	}
}

func TestClampSummary(t *testing.T) {
	t.Run("under cap", func(t *testing.T) {
		if got := clampSummary("hello", 100); got != "hello" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("over cap utf8", func(t *testing.T) {
		s := strings.Repeat("中", 100) // each rune = 3 bytes
		out := clampSummary(s, 50)
		if len(out) > 50 {
			t.Errorf("len=%d > 50", len(out))
		}
		// Result must remain valid utf-8: every rune is 3 bytes so
		// len % 3 == 0 after clamping on a starter byte.
		if len(out)%3 != 0 {
			t.Errorf("clamped on non-starter byte: len=%d", len(out))
		}
	})
	t.Run("zero cap", func(t *testing.T) {
		if got := clampSummary("hello", 0); got != "hello" {
			t.Errorf("got %q", got)
		}
	})
}

func TestErrPromoterNotConfigured(t *testing.T) {
	if ErrPromoterNotConfigured() == nil {
		t.Error("ErrPromoterNotConfigured returned nil")
	}
	if !errors.Is(ErrPromoterNotConfigured(), errPromoterNotConfigured) {
		t.Error("ErrPromoterNotConfigured does not match the sentinel")
	}
}
