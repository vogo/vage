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

package sessionview

import (
	"context"
	"errors"
	"testing"
)

func TestValidate_EmptyChildSession(t *testing.T) {
	v := &SessionView{ParentSessionID: "p"}
	if err := v.Validate(); err == nil {
		t.Errorf("expected error on empty ChildSessionID")
	}
}

func TestValidate_NilReceiver(t *testing.T) {
	var v *SessionView
	if err := v.Validate(); !errors.Is(err, ErrInvalidView) {
		t.Errorf("nil view: err = %v, want ErrInvalidView", err)
	}
}

func TestValidate_OK(t *testing.T) {
	v := &SessionView{ChildSessionID: "c", Subgoal: "x"}
	if err := v.Validate(); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestClone_Independence(t *testing.T) {
	src := &SessionView{
		ParentSessionID: "p",
		ChildSessionID:  "c",
		Subgoal:         "go",
		ScratchSlot:     "slot",
		NotesIndex:      []string{"a", "b"},
		Metadata:        map[string]any{"k": "v"},
	}
	dst := src.Clone()

	dst.NotesIndex[0] = "MODIFIED"
	dst.Metadata["k"] = "MODIFIED"

	if src.NotesIndex[0] != "a" {
		t.Errorf("clone leaked into src.NotesIndex: %v", src.NotesIndex)
	}
	if src.Metadata["k"] != "v" {
		t.Errorf("clone leaked into src.Metadata: %v", src.Metadata)
	}
}

func TestClone_NilReceiver(t *testing.T) {
	var v *SessionView
	if got := v.Clone(); got != nil {
		t.Errorf("clone of nil = %+v, want nil", got)
	}
}

func TestContext_RoundTrip(t *testing.T) {
	v := &SessionView{ChildSessionID: "c"}
	ctx := WithContext(context.Background(), v)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatalf("FromContext: not found")
	}
	if got.ChildSessionID != "c" {
		t.Errorf("ChildSessionID = %q, want c", got.ChildSessionID)
	}
}

func TestContext_NilViewIsPassThrough(t *testing.T) {
	ctx := WithContext(context.Background(), nil)
	if _, ok := FromContext(ctx); ok {
		t.Errorf("nil view should yield no-view ctx")
	}
}

func TestContext_NilCtx(t *testing.T) {
	// FromContext defensively accepts a typed-nil ctx so that buggy
	// callers do not panic. Use a typed nil here rather than the bare
	// literal so the linter does not complain about passing nil.
	var ctx context.Context
	if _, ok := FromContext(ctx); ok {
		t.Errorf("nil ctx should yield no-view")
	}
}

func TestContext_FromContextEmpty(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Errorf("empty ctx should yield no view")
	}
}
