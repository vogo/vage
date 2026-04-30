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

package checkpoint

import (
	"context"
	"testing"

	"github.com/vogo/aimodel"
)

func TestMapIterationStore_Conformance(t *testing.T) {
	runStoreContract(t, "map", func(_ *testing.T) IterationStore {
		return NewMapIterationStore()
	})
}

// TestMapIterationStore_LoadReturnsCopy guards against external code
// mutating the returned checkpoint and bleeding back into the store.
func TestMapIterationStore_LoadReturnsCopy(t *testing.T) {
	s := NewMapIterationStore()
	ctx := context.Background()
	cp := newTestCheckpoint("sess-cp", 0, false, "")
	if err := s.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(ctx, "sess-cp", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Mutate the loaded copy's messages.
	loaded.Messages = append(loaded.Messages, aimodel.Message{
		Role:    aimodel.RoleUser,
		Content: aimodel.NewTextContent("INJECTED"),
	})

	again, err := s.Load(ctx, "sess-cp", "")
	if err != nil {
		t.Fatalf("Load again: %v", err)
	}
	if len(again.Messages) != 2 {
		t.Errorf("internal state mutated: messages count = %d, want 2", len(again.Messages))
	}
}
