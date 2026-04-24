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

package schema

import (
	"context"
	"testing"
)

func TestWithSessionID_Empty(t *testing.T) {
	ctx := context.Background()
	out := WithSessionID(ctx, "")
	if out != ctx {
		t.Fatalf("WithSessionID with empty string must return the original ctx")
	}
	if got := SessionIDFromContext(out); got != "" {
		t.Fatalf("expected empty sessionID, got %q", got)
	}
}

func TestWithSessionID_RoundTrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "sess-abc")
	if got := SessionIDFromContext(ctx); got != "sess-abc" {
		t.Fatalf("expected %q, got %q", "sess-abc", got)
	}
}

func TestWithEmitter_Nil(t *testing.T) {
	ctx := context.Background()
	out := WithEmitter(ctx, nil)
	if out != ctx {
		t.Fatalf("WithEmitter(nil) must return the original ctx")
	}
	if EmitterFromContext(out) != nil {
		t.Fatalf("expected nil emitter")
	}
}

func TestWithEmitter_RoundTrip(t *testing.T) {
	var captured Event
	em := Emitter(func(e Event) error {
		captured = e
		return nil
	})
	ctx := WithEmitter(context.Background(), em)
	got := EmitterFromContext(ctx)
	if got == nil {
		t.Fatalf("expected emitter to be set")
	}
	_ = got(Event{Type: "x"})
	if captured.Type != "x" {
		t.Fatalf("captured event not delivered through emitter; got %q", captured.Type)
	}
}

func TestMissingValues_ReturnZero(t *testing.T) {
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Fatalf("expected empty sessionID for bare ctx, got %q", got)
	}
	if got := EmitterFromContext(ctx); got != nil {
		t.Fatalf("expected nil emitter for bare ctx")
	}
}
