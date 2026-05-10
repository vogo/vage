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

package tool

import (
	"encoding/json"
	"testing"
)

// stubTracker is a minimal ResourceTracker used to verify the interface
// can be satisfied by simple tool implementations.
type stubTracker struct {
	refs []ResourceRef
}

func (s *stubTracker) ResourceIDs(_ map[string]any) []ResourceRef {
	return s.refs
}

func TestResourceMode_ConstantValues(t *testing.T) {
	// The string values are part of the public contract — they are
	// emitted in debug logs and may appear in serialized events. Pin
	// them so a refactor that flips the spelling fails loudly.
	if ResourceRead != "read" {
		t.Errorf("ResourceRead = %q, want %q", ResourceRead, "read")
	}
	if ResourceWrite != "write" {
		t.Errorf("ResourceWrite = %q, want %q", ResourceWrite, "write")
	}
}

func TestResourceTracker_InterfaceSatisfaction(t *testing.T) {
	// Compile-time check that stub satisfies the interface; the runtime
	// assignment additionally guards against a future change that drops
	// ResourceIDs from the interface.
	var tracker ResourceTracker = &stubTracker{
		refs: []ResourceRef{{ID: "/tmp/a", Mode: ResourceRead}},
	}

	got := tracker.ResourceIDs(nil)
	if len(got) != 1 {
		t.Fatalf("ResourceIDs len = %d, want 1", len(got))
	}
	if got[0].ID != "/tmp/a" || got[0].Mode != ResourceRead {
		t.Errorf("ResourceIDs[0] = %+v, want {ID:/tmp/a Mode:read}", got[0])
	}
}

func TestResourceTracker_NilSafe(t *testing.T) {
	// ResourceIDs must tolerate nil and empty arg maps without panic;
	// returning nil signals "no resource touched", which is the right
	// fallback for malformed invocations.
	var tracker ResourceTracker = &stubTracker{refs: nil}

	if got := tracker.ResourceIDs(nil); got != nil {
		t.Errorf("ResourceIDs(nil) = %v, want nil", got)
	}
	if got := tracker.ResourceIDs(map[string]any{}); got != nil {
		t.Errorf("ResourceIDs(empty) = %v, want nil", got)
	}
}

func TestResourceRef_JSONRoundTrip(t *testing.T) {
	// ResourceRef is intentionally JSON-serialisable so trace tooling
	// and event payloads can capture the resource set without bespoke
	// encoders. Round-trip a representative value to lock the wire form.
	in := ResourceRef{ID: "/abs/path/to/file.go", Mode: ResourceWrite}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	const want = `{"id":"/abs/path/to/file.go","mode":"write"}`
	if string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var out ResourceRef
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip = %+v, want %+v", out, in)
	}
}

func TestResourceRef_ZeroValueLegal(t *testing.T) {
	// The zero ResourceRef is meaningless but legal — consumers should
	// treat empty ID as "skip". This test exists so a future change
	// that introduces required-field validation is forced to revisit
	// the contract documented on the type.
	ref := ResourceRef{}
	if ref.ID != "" || ref.Mode != "" {
		t.Errorf("zero value = %+v, want empty", ref)
	}
}
