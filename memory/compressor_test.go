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

	"github.com/vogo/vagent/schema"
)

func TestSlidingWindowCompressor_UnderWindow(t *testing.T) {
	c := NewSlidingWindowCompressor(5)
	ctx := context.Background()

	msgs := []schema.Message{
		schema.NewUserMessage("hello"),
		schema.NewUserMessage("world"),
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
		schema.NewUserMessage("first"),
		schema.NewUserMessage("second"),
		schema.NewUserMessage("third"),
		schema.NewUserMessage("fourth"),
	}

	result, err := c.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Compress len = %d, want 2", len(result))
	}
	if result[0].Content.Text() != "third" {
		t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "third")
	}
	if result[1].Content.Text() != "fourth" {
		t.Errorf("result[1] = %q, want %q", result[1].Content.Text(), "fourth")
	}
}

func TestSlidingWindowCompressor_ExactWindow(t *testing.T) {
	c := NewSlidingWindowCompressor(3)
	ctx := context.Background()

	msgs := []schema.Message{
		schema.NewUserMessage("a"),
		schema.NewUserMessage("b"),
		schema.NewUserMessage("c"),
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

	_, err := c.Compress(ctx, []schema.Message{schema.NewUserMessage("hi")}, 0)
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
		schema.NewUserMessage("a"),
		schema.NewUserMessage("b"),
	}

	result, err := f.Compress(ctx, msgs, 0)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Compress len = %d, want 1", len(result))
	}
	if result[0].Content.Text() != "b" {
		t.Errorf("result[0] = %q, want %q", result[0].Content.Text(), "b")
	}
}
