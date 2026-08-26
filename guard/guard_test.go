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

package guard

import (
	"errors"
	"testing"
)

func TestPass(t *testing.T) {
	r := Pass()
	if r.Action != ActionPass {
		t.Fatalf("Pass().Action = %q, want %q", r.Action, ActionPass)
	}
}

func TestBlock(t *testing.T) {
	r := Block("test_guard", "bad input", "rule1", "rule2")
	if r.Action != ActionBlock {
		t.Fatalf("Block().Action = %q, want %q", r.Action, ActionBlock)
	}

	if r.GuardName != "test_guard" {
		t.Errorf("Block().GuardName = %q, want %q", r.GuardName, "test_guard")
	}

	if r.Reason != "bad input" {
		t.Errorf("Block().Reason = %q, want %q", r.Reason, "bad input")
	}

	if len(r.Violations) != 2 || r.Violations[0] != "rule1" || r.Violations[1] != "rule2" {
		t.Errorf("Block().Violations = %v, want [rule1 rule2]", r.Violations)
	}
}

func TestBlock_NoViolations(t *testing.T) {
	r := Block("g", "reason")
	if len(r.Violations) != 0 {
		t.Errorf("Block() with no violations: Violations = %v, want empty", r.Violations)
	}
}

func TestRewrite(t *testing.T) {
	r := Rewrite("pii", "redacted text", "PII found", "email", "phone")
	if r.Action != ActionRewrite {
		t.Fatalf("Rewrite().Action = %q, want %q", r.Action, ActionRewrite)
	}

	if r.GuardName != "pii" {
		t.Errorf("Rewrite().GuardName = %q, want %q", r.GuardName, "pii")
	}

	if r.Content != "redacted text" {
		t.Errorf("Rewrite().Content = %q, want %q", r.Content, "redacted text")
	}

	if r.Reason != "PII found" {
		t.Errorf("Rewrite().Reason = %q, want %q", r.Reason, "PII found")
	}

	if len(r.Violations) != 2 || r.Violations[0] != "email" || r.Violations[1] != "phone" {
		t.Errorf("Rewrite().Violations = %v, want [email phone]", r.Violations)
	}
}

func TestRewrite_NoViolations(t *testing.T) {
	r := Rewrite("g", "content", "reason")
	if len(r.Violations) != 0 {
		t.Errorf("Rewrite() with no violations: Violations = %v, want empty", r.Violations)
	}
}

func TestBlockedError(t *testing.T) {
	r := Block("pii", "PII detected", "email")
	err := &BlockedError{Result: r}

	if err.Error() != "blocked by pii: PII detected" {
		t.Errorf("BlockedError.Error() = %q, want %q", err.Error(), "blocked by pii: PII detected")
	}

	if _, ok := errors.AsType[*BlockedError](err); !ok {
		t.Errorf("errors.As failed for BlockedError")
	}
}

func TestNewInputMessage(t *testing.T) {
	msg := NewInputMessage("hello")
	if msg.Direction != DirectionInput {
		t.Errorf("Direction = %v, want DirectionInput", msg.Direction)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}
}

func TestNewOutputMessage(t *testing.T) {
	msg := NewOutputMessage("hello")
	if msg.Direction != DirectionOutput {
		t.Errorf("Direction = %v, want DirectionOutput", msg.Direction)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}
}
