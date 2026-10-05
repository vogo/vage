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

package interrupt

import (
	"context"
	"testing"
)

func TestApprovedCalls_FailClosed(t *testing.T) {
	if IsApprovedCall(context.Background(), "a") {
		t.Fatal("plain context must not approve a call")
	}
	var nilCtx context.Context
	if IsApprovedCall(nilCtx, "a") {
		t.Fatal("nil context must not approve a call")
	}
	ctx := WithApprovedCalls(context.Background(), []string{"a"})
	if IsApprovedCall(ctx, "") {
		t.Fatal("empty id must not be approved")
	}
	if IsApprovedCall(ctx, "b") {
		t.Fatal("id outside the set must not be approved")
	}
}

func TestApprovedCalls_SetAndChild(t *testing.T) {
	ids := []string{"a", "", "a"}
	var nilCtx context.Context
	ctx := WithApprovedCalls(nilCtx, ids)
	ids[0] = "mutated"

	if !IsApprovedCall(ctx, "a") {
		t.Fatal("id a must be approved")
	}
	if IsApprovedCall(ctx, "") {
		t.Fatal("empty id must not be stored")
	}
	if IsApprovedCall(ctx, "mutated") {
		t.Fatal("caller slice must not alias the stored set")
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if !IsApprovedCall(child, "a") {
		t.Fatal("derived context must keep the approved set")
	}
}

func TestExecutingCall_RoundTrip(t *testing.T) {
	var nilCtx context.Context
	if ExecutingCallID(nilCtx) != "" {
		t.Fatal("nil context has no executing id")
	}
	if ExecutingCallID(context.Background()) != "" {
		t.Fatal("plain context has no executing id")
	}

	ctx := WithExecutingCall(nilCtx, "call-1")
	if got := ExecutingCallID(ctx); got != "call-1" {
		t.Fatalf("ExecutingCallID = %q, want call-1", got)
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := ExecutingCallID(child); got != "call-1" {
		t.Fatalf("child ExecutingCallID = %q, want call-1", got)
	}

	other := WithExecutingCall(ctx, "call-2")
	if got := ExecutingCallID(ctx); got != "call-1" {
		t.Fatalf("parent ExecutingCallID = %q, want call-1 (child must not write back)", got)
	}
	if got := ExecutingCallID(other); got != "call-2" {
		t.Fatalf("child ExecutingCallID = %q, want call-2", got)
	}
}
