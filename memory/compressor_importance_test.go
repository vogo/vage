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
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

func newSystemMessage(text string) schema.Message {
	return schema.Message{
		Message:   schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleSystem, text),
		Timestamp: time.Now(),
	}
}

func newAssistantMessage(text string) schema.Message {
	return schema.Message{
		Message:   schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, text),
		Timestamp: time.Now(),
	}
}

func newToolMessage(text string) schema.Message {
	return schema.Message{
		Message:   schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleTool, text),
		Timestamp: time.Now(),
	}
}

func TestImportanceRankingCompressor(t *testing.T) {
	c := NewImportanceRankingCompressorWithDefaults()

	t.Run("mixed roles budget forces drops", func(t *testing.T) {
		msgs := []schema.Message{
			newSystemMessage("sys"),                      // 1 token, score ~1000
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "usr1"),                // 1 token, score ~50
			newAssistantMessage(strings.Repeat("a", 40)), // 10 tokens, score ~10
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "usr2"),                // 1 token, score ~50
		}
		// Budget=3: system(1) + usr1(1) + usr2(1) = 3, assistant(10) skipped
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
		if result[0].Role() != schema.RoleSystem {
			t.Errorf("result[0] role = %q, want system", result[0].Role)
		}
	})

	t.Run("system always retained first", func(t *testing.T) {
		msgs := []schema.Message{
			newSystemMessage("sys"),           // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "user msg"), // 1 token
		}
		// Budget=1: system message fits, user doesn't
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1", len(result))
		}
		if result[0].Role() != schema.RoleSystem {
			t.Errorf("expected system message retained, got role %q", result[0].Role)
		}
	})

	t.Run("recency tie breaking", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"), // 1 token, user, index 0
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"), // 1 token, user, index 1
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccc"), // 1 token, user, index 2
		}
		// Budget=2: last 2 user msgs should be kept (higher recency bonus)
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "bbbb" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "bbbb")
		}
		if result[1].Text() != "cccc" {
			t.Errorf("result[1] = %q, want %q", result[1].Text(), "cccc")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result, err := c.Compress(context.Background(), []schema.Message{}, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("got %d messages, want 0", len(result))
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := c.Compress(ctx, []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}, 100)
		if err == nil {
			t.Error("expected error for cancelled context")
		}
	})

	t.Run("unlimited maxTokens=0", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "d"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "e"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 5 {
			t.Fatalf("got %d messages, want 5", len(result))
		}
	})

	t.Run("chronological order preserved", func(t *testing.T) {
		msgs := []schema.Message{
			newAssistantMessage("early"),  // low score
			newSystemMessage("sys"),       // high score
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "mid"),  // medium score
			newToolMessage("tool"),        // medium-high score
			newAssistantMessage("recent"), // low score
		}
		// Budget enough for system + tool + user (3 tokens)
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Verify chronological order: indices should be ascending
		for i := 1; i < len(result); i++ {
			if result[i].Timestamp.Before(result[i-1].Timestamp) {
				t.Error("messages not in chronological order")
			}
		}
	})

	t.Run("default constructor", func(t *testing.T) {
		dc := NewImportanceRankingCompressorWithDefaults()
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "test"),
		}
		result, err := dc.Compress(context.Background(), msgs, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1", len(result))
		}
	})

	t.Run("custom scorer", func(t *testing.T) {
		// Scorer that returns same score for all messages - stable sort should preserve order.
		constantScorer := func(_ []schema.Message, _ int) float64 {
			return 42.0
		}
		cc := NewImportanceRankingCompressor(constantScorer)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"), // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"), // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccc"), // 1 token
		}
		// Budget=2: stable sort preserves original order, so first 2 should be selected
		result, err := cc.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "aaaa" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "aaaa")
		}
		if result[1].Text() != "bbbb" {
			t.Errorf("result[1] = %q, want %q", result[1].Text(), "bbbb")
		}
	})

	t.Run("constructor panics on nil scorer", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for nil scorer")
			}
		}()
		NewImportanceRankingCompressor(nil)
	})
}
