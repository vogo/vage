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
	"testing"

	"github.com/vogo/vage/schema"
)

func TestSlidingWindowCompressor_UnderWindow(t *testing.T) {
	c := NewSlidingWindowCompressor(5)
	ctx := context.Background()

	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "world"),
	}

	result, err := c.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Compress len = %d, want 2", len(result))
	}
}

func TestSlidingWindowCompressor_OverWindow(t *testing.T) {
	c := NewSlidingWindowCompressor(2)
	ctx := context.Background()

	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "first"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "second"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "third"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "fourth"),
	}

	result, err := c.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Compress len = %d, want 2", len(result))
	}
	if result[0].Text() != "third" {
		t.Errorf("result[0] = %q, want %q", result[0].Text(), "third")
	}
	if result[1].Text() != "fourth" {
		t.Errorf("result[1] = %q, want %q", result[1].Text(), "fourth")
	}
}

func TestSlidingWindowCompressor_ExactWindow(t *testing.T) {
	c := NewSlidingWindowCompressor(3)
	ctx := context.Background()

	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
	}

	result, err := c.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("Compress len = %d, want 3", len(result))
	}
}

func TestSlidingWindowCompressor_ContextCanceled(t *testing.T) {
	c := NewSlidingWindowCompressor(5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Compress(ctx, []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}, 0)
	if err == nil {
		t.Error("expected error for canceled context")
	}
}

func TestSlidingWindowCompressor_ZeroWindowPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for zero window size")
		}
	}()

	NewSlidingWindowCompressor(0)
}

func TestSlidingWindowCompressor_NegativeWindowPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative window size")
		}
	}()

	NewSlidingWindowCompressor(-1)
}

func TestSlidingWindowCompressor_MaxTokens(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		c := NewSlidingWindowCompressor(3)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbbbbbb"), // 2 tokens
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccc"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "dddd"),     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "eeee"),     // 1 token
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d messages, want 3", len(result))
		}
	})

	t.Run("trims within window", func(t *testing.T) {
		c := NewSlidingWindowCompressor(5)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),                 // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbbbbbb"),             // 2 tokens
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccccccccccccccccccc"), // 5 tokens
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "dddd"),                 // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "eeee"),                 // 1 token
		}
		// Budget=2 fits last 2 messages (1+1=2)
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "dddd" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "dddd")
		}
		if result[1].Text() != "eeee" {
			t.Errorf("result[1] = %q, want %q", result[1].Text(), "eeee")
		}
	})

	t.Run("single oversized message", func(t *testing.T) {
		c := NewSlidingWindowCompressor(3)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "this is a long message that exceeds the budget"),
		}
		result, err := c.Compress(context.Background(), msgs, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d messages, want 1", len(result))
		}
	})

	t.Run("empty input with maxTokens", func(t *testing.T) {
		c := NewSlidingWindowCompressor(3)
		result, err := c.Compress(context.Background(), []schema.Message{}, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("got %d messages, want 0", len(result))
		}
	})
}

func TestCompressFunc(t *testing.T) {
	// Custom compressor that drops all but the last message.
	f := CompressFunc(func(_ context.Context, msgs []schema.Message, _ int) ([]schema.Message, error) {
		if len(msgs) == 0 {
			return msgs, nil
		}
		return msgs[len(msgs)-1:], nil
	})

	ctx := context.Background()
	msgs := []schema.Message{
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
		schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
	}

	result, err := f.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Compress len = %d, want 1", len(result))
	}
	if result[0].Text() != "b" {
		t.Errorf("result[0] = %q, want %q", result[0].Text(), "b")
	}
}
