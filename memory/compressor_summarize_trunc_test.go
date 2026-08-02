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
	"errors"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

func TestSummarizeAndTruncCompressor(t *testing.T) {
	mockSummarizer := func(_ context.Context, msgs []schema.Message) (string, error) {
		return "summary of older messages", nil
	}

	t.Run("no summarization needed", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 5)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	t.Run("summarization triggered", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 2)
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
		// 1 summary + 2 recent = 3
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
		if result[0].Text() != "summary of older messages" {
			t.Errorf("summary = %q, want %q", result[0].Text(), "summary of older messages")
		}
		if result[1].Text() != "d" {
			t.Errorf("result[1] = %q, want %q", result[1].Text(), "d")
		}
		if result[2].Text() != "e" {
			t.Errorf("result[2] = %q, want %q", result[2].Text(), "e")
		}
	})

	t.Run("summary metadata", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 2)
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

		summaryMsg := result[0]
		if compressed, ok := summaryMsg.Metadata["compressed"].(bool); !ok || !compressed {
			t.Error("expected compressed=true in metadata")
		}
		if sourceCount, ok := summaryMsg.Metadata["source_count"].(int); !ok || sourceCount != 3 {
			t.Errorf("expected source_count=3, got %v", summaryMsg.Metadata["source_count"])
		}
		if strategy, ok := summaryMsg.Metadata["strategy"].(string); !ok || strategy != "summarize_and_trunc" {
			t.Errorf("expected strategy=summarize_and_trunc, got %v", summaryMsg.Metadata["strategy"])
		}
	})

	t.Run("empty summary text", func(t *testing.T) {
		emptySummarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "", nil
		}
		c := NewSummarizeAndTruncCompressor(emptySummarizer, 2)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "d"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// No summary message, just 2 recent
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "c" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "c")
		}
	})

	t.Run("summarizer error propagated", func(t *testing.T) {
		errSummarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "", errors.New("summarizer failed")
		}
		c := NewSummarizeAndTruncCompressor(errSummarizer, 1)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
		}
		_, err := c.Compress(context.Background(), msgs, 0)
		if err == nil {
			t.Error("expected error from summarizer")
		}
		if err.Error() != "summarizer failed" {
			t.Errorf("error = %q, want %q", err.Error(), "summarizer failed")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := c.Compress(ctx, []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}, 0)
		if err == nil {
			t.Error("expected error for cancelled context")
		}
	})

	t.Run("keepLastN equals len", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 3)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	t.Run("custom summary role", func(t *testing.T) {
		c := NewSummarizeAndTruncCompressor(mockSummarizer, 1, WithSummaryRole(schema.RoleSystem))
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0].Role() != schema.RoleSystem {
			t.Errorf("summary role = %q, want %q", result[0].Role, schema.RoleSystem)
		}
	})

	t.Run("maxTokens truncates summary", func(t *testing.T) {
		longSummarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "this is a very long summary text that should be truncated", nil
		}
		c := NewSummarizeAndTruncCompressor(longSummarizer, 1)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "old1"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "old2"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"), // 1 token recent
		}
		// Budget=3: recent uses 1 token, summary budget=2 tokens (~8 chars)
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2 (summary + recent)", len(result))
		}
		// Summary should be truncated
		summaryText := result[0].Text()
		fullText := "this is a very long summary text that should be truncated"
		if len(summaryText) >= len(fullText) {
			t.Errorf("summary should be truncated, got len=%d", len(summaryText))
		}
	})

	t.Run("maxTokens no room for summary", func(t *testing.T) {
		longSummarizer := func(_ context.Context, _ []schema.Message) (string, error) {
			return "summary", nil
		}
		c := NewSummarizeAndTruncCompressor(longSummarizer, 1)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "old"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaaaaaabbbbbbbb"), // 4 tokens recent
		}
		// Budget=2: recent uses 4 tokens, exceeds budget → no room for summary
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1 (recent only)", len(result))
		}
		if result[0].Text() != "aaaaaaaabbbbbbbb" {
			t.Errorf("result[0] = %q, want recent message", result[0].Text())
		}
	})

	t.Run("constructor panics on nil summarizer", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for nil summarizer")
			}
		}()
		NewSummarizeAndTruncCompressor(nil, 5)
	})

	t.Run("constructor panics on zero keepLastN", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for zero keepLastN")
			}
		}()
		NewSummarizeAndTruncCompressor(mockSummarizer, 0)
	})
}
