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

import "context"

type approvedCallsKey struct{}

type executingCallKey struct{}

// WithApprovedCalls returns a child context whose approved set is the
// non-empty ids in ids. The set is copied into a map; later mutation of
// ids does not change the context. A nil ctx is treated as
// context.Background so context.WithValue does not panic.
//
// Only tool calls the human approved for handler execution belong in the
// set (pending, Execute, and not IsError). Sibling calls in the same
// batch must be left out. Host permission layers consult IsApprovedCall
// with ExecutingCallID rather than treating the whole batch as approved.
func WithApprovedCalls(ctx context.Context, ids []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return context.WithValue(ctx, approvedCallsKey{}, set)
}

// IsApprovedCall reports whether toolCallID is in the approved set stored
// on ctx. It returns false when ctx is nil, toolCallID is empty, or the
// id is absent. Absence is fail-closed: a call that did not pass through
// WithApprovedCalls is not approved.
func IsApprovedCall(ctx context.Context, toolCallID string) bool {
	if ctx == nil || toolCallID == "" {
		return false
	}
	set, ok := ctx.Value(approvedCallsKey{}).(map[string]struct{})
	if !ok {
		return false
	}
	_, ok = set[toolCallID]
	return ok
}

// WithExecutingCall returns a child context that names the tool call
// currently entering Registry.Execute. Each call in a parallel batch must
// derive its own child; the parent batch context is left unchanged. A nil
// ctx is treated as context.Background.
func WithExecutingCall(ctx context.Context, toolCallID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executingCallKey{}, toolCallID)
}

// ExecutingCallID returns the tool call id stored by WithExecutingCall.
// It returns an empty string when ctx is nil or no id was stored. An
// empty id is not an approved execution: IsApprovedCall rejects it.
func ExecutingCallID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(executingCallKey{}).(string)
	return id
}
