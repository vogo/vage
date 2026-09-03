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
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestBudgetValidate(t *testing.T) {
	if err := (Budget{}).Validate(); err != nil {
		t.Fatalf("zero budget: %v", err)
	}
	if err := (Budget{ModelContextTokens: 100, ReservedOutput: 10}).Validate(); err != nil {
		t.Fatalf("positive: %v", err)
	}

	cases := []Budget{
		{ModelContextTokens: -1},
		{ReservedOutput: -1},
		{ReservedTools: -1},
		{ReservedSystem: -1},
		{AvailableHistory: -1},
	}
	for _, b := range cases {
		if err := b.Validate(); err == nil {
			t.Errorf("expected error for %#v", b)
		}
	}
}

func TestBudgetUnlimitedVsZeroHistory(t *testing.T) {
	unlimited := Budget{}
	if !unlimited.Unlimited() {
		t.Error("zero ModelContextTokens should be unlimited")
	}
	if unlimited.BoundedZeroHistory() {
		t.Error("unlimited must not report bounded zero history")
	}

	zero := Budget{ModelContextTokens: 100, AvailableHistory: 0}
	if zero.Unlimited() {
		t.Error("bounded budget is not unlimited")
	}
	if !zero.BoundedZeroHistory() {
		t.Error("AvailableHistory=0 with a window is bounded zero history")
	}

	positive := Budget{ModelContextTokens: 100, AvailableHistory: 40}
	if positive.Unlimited() || positive.BoundedZeroHistory() {
		t.Error("positive remaining is neither unlimited nor zero-history")
	}
}

func TestBudgetEstimatorOrDefault(t *testing.T) {
	if (Budget{}).EstimatorOrDefault() == nil {
		t.Fatal("nil estimator should fall back")
	}

	called := false
	est := func(schema.Message) int {
		called = true
		return 7
	}
	b := Budget{Estimator: est}
	if got := b.EstimatorOrDefault()(schema.NewUserMessage(schema.ProtocolOpenAIChat, "x")); got != 7 {
		t.Errorf("got %d, want 7", got)
	}
	if !called {
		t.Error("custom estimator not used")
	}
}

func TestCompressWithBudget_LegacyNeverSeesZeroWhenBoundedEmpty(t *testing.T) {
	var seen []int
	legacy := CompressFunc(func(_ context.Context, msgs []schema.Message, maxTokens int) ([]schema.Message, error) {
		seen = append(seen, maxTokens)
		return msgs, nil
	})

	msgs := []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")}

	out, err := CompressWithBudget(context.Background(), legacy, CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 100, AvailableHistory: 0},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("bounded zero remaining should return empty, got %d", len(out))
	}
	if len(seen) != 0 {
		t.Fatalf("legacy compressor called with maxTokens=%v; want not called", seen)
	}

	out, err = CompressWithBudget(context.Background(), legacy, CompressionInput{
		Messages: msgs,
		Budget:   Budget{},
	})
	if err != nil {
		t.Fatalf("unlimited err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("unlimited should keep history, got %d", len(out))
	}
	if len(seen) != 1 || seen[0] != 0 {
		t.Fatalf("unlimited should pass maxTokens=0, got %v", seen)
	}

	seen = nil
	out, err = CompressWithBudget(context.Background(), legacy, CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 100, AvailableHistory: 12},
	})
	if err != nil {
		t.Fatalf("positive err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("positive remaining should call legacy, got %d", len(out))
	}
	if len(seen) != 1 || seen[0] != 12 {
		t.Fatalf("legacy maxTokens=%v, want [12]", seen)
	}
}

func TestChainCompressor_MixedBudgetAwareAndLegacy(t *testing.T) {
	var legacyCalls int
	legacy := CompressFunc(func(_ context.Context, msgs []schema.Message, maxTokens int) ([]schema.Message, error) {
		legacyCalls++
		if maxTokens == 0 {
			t.Errorf("legacy received maxTokens=0 inside a bounded chain")
		}
		return msgs, nil
	})

	chain := NewChainCompressor(NewSlidingWindowCompressor(10), legacy)
	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("a", 16)),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("b", 16)),
	}

	out, err := chain.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 80, AvailableHistory: 8},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d messages", len(out))
	}
	if legacyCalls != 1 {
		t.Fatalf("legacy calls=%d, want 1", legacyCalls)
	}

	legacyCalls = 0
	out, err = chain.CompressWithBudget(context.Background(), CompressionInput{
		Messages: msgs,
		Budget:   Budget{ModelContextTokens: 80, AvailableHistory: 0},
	})
	if err != nil {
		t.Fatalf("zero remaining err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("zero remaining should empty the chain, got %d", len(out))
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy should not run on bounded zero remaining, calls=%d", legacyCalls)
	}
}

func TestEstimateToolDefsDeterministic(t *testing.T) {
	defs := []schema.ToolDef{
		{Name: "search", Description: "find things", Parameters: map[string]any{"type": "object"}},
		{Name: "ping", Description: "pong"},
	}
	a := EstimateToolDefs(defs, nil)
	b := EstimateToolDefs(defs, nil)
	if a != b || a <= 0 {
		t.Fatalf("EstimateToolDefs = %d / %d, want identical positive", a, b)
	}
	if EstimateToolDefs(nil, nil) != 0 {
		t.Fatal("empty defs should estimate 0")
	}
}

func TestEstimateMessages(t *testing.T) {
	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("x", 16)), // 4
		schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("y", 8)),  // 2
	}
	if got := EstimateMessages(nil, msgs); got != 6 {
		t.Fatalf("EstimateMessages = %d, want 6", got)
	}
}
