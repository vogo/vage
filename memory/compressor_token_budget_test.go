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

func TestTokenBudgetCompressor(t *testing.T) {
	c := NewTokenBudgetCompressor()

	t.Run("under budget", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"), // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"), // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccc"), // 1 token
		}
		result, err := c.Compress(context.Background(), msgs, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	t.Run("exact budget", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbbbbbb"), // 2 tokens
		}
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
	})

	t.Run("over budget", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccccccc"), // 2 tokens
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "dddd"),     // 1 token
		}
		// Budget=3 fits last 2 (2+1=3)
		result, err := c.Compress(context.Background(), msgs, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "cccccccc" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "cccccccc")
		}
		if result[1].Text() != "dddd" {
			t.Errorf("result[1] = %q, want %q", result[1].Text(), "dddd")
		}
	})

	t.Run("single oversized message", func(t *testing.T) {
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("x", 100)), // 25 tokens
		}
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1", len(result))
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
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccc"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "dddd"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "eeee"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 5 {
			t.Fatalf("got %d messages, want 5", len(result))
		}
	})
}
