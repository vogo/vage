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

	"github.com/vogo/vage/schema"
)

func TestChainCompressor(t *testing.T) {
	t.Run("empty chain returns input", func(t *testing.T) {
		c := NewChainCompressor()
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "world"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
	})

	t.Run("single compressor", func(t *testing.T) {
		c := NewChainCompressor(NewSlidingWindowCompressor(2))
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "a"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "b"),
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "c"),
		}
		result, err := c.Compress(context.Background(), msgs, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d messages, want 2", len(result))
		}
		if result[0].Text() != "b" {
			t.Errorf("result[0] = %q, want %q", result[0].Text(), "b")
		}
	})

	t.Run("sliding window then token budget", func(t *testing.T) {
		c := NewChainCompressor(
			NewSlidingWindowCompressor(4),
			NewTokenBudgetCompressor(),
		)
		msgs := []schema.Message{
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "aaaa"),                                     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "bbbb"),                                     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "cccccccccccccccccccc"),                     // 5 tokens
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "dddd"),                                     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "eeee"),                                     // 1 token
			schema.NewUserMessage(schema.ProtocolOpenAIChat, "ffffffffffffffffffffffffffffffffffffffff"), // 10 tokens
		}
		// Window=4: keeps [cccc...(5), dddd(1), eeee(1), ffff...(10)]
		// Budget=2: from newest, ffff(10) exceeds budget alone → at least 1 guaranteed
		result, err := c.Compress(context.Background(), msgs, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) < 1 {
			t.Fatal("expected at least one message")
		}
	})

	t.Run("error propagation", func(t *testing.T) {
		failing := CompressFunc(func(_ context.Context, _ []schema.Message, _ int) ([]schema.Message, error) {
			return nil, errors.New("compressor failed")
		})
		c := NewChainCompressor(
			NewSlidingWindowCompressor(5),
			failing,
		)
		msgs := []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "test")}
		_, err := c.Compress(context.Background(), msgs, 0)
		if err == nil {
			t.Error("expected error from chain")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		c := NewChainCompressor(
			NewSlidingWindowCompressor(5),
			NewTokenBudgetCompressor(),
		)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Compress(ctx, []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hi")}, 0)
		if err == nil {
			t.Error("expected error for cancelled context")
		}
	})
}
