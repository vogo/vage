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
	"encoding/json"
	"errors"
	"testing"
)

func TestMapStore_Contract(t *testing.T) {
	runStoreContract(t, "MapStore", func(t *testing.T) Store {
		t.Helper()
		return NewMapStore()
	})
}

func TestMapStore_V1RejectedByV2Reader(t *testing.T) {
	s := NewMapStore()
	ctx := context.Background()
	rec := newTestRecord("sess-v1", []string{"call-1"})
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s.mu.Lock()
	s.data[rec.ID].Version = 1
	s.mu.Unlock()

	if _, err := s.Get(ctx, rec.ID); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("Get v1 record err = %v, want ErrUnknownVersion", err)
	}
}

func TestEffectiveParams_V2EmptyToolFilterRoundTrip(t *testing.T) {
	p := EffectiveParams{
		Model:         "m",
		MaxIterations: 10,
		ToolMode:      "none",
		ToolFilter:    []string{},
		StopSequences: nil,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("invalid JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("map Unmarshal: %v", err)
	}
	if _, ok := decoded["tool_filter"]; !ok {
		t.Fatal("empty tool_filter must be persisted, not omitted")
	}

	var got EffectiveParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ToolMode != "none" {
		t.Errorf("ToolMode = %q, want none", got.ToolMode)
	}
	if got.ToolFilter == nil {
		t.Fatal("ToolFilter is nil after round-trip")
	}
	if len(got.ToolFilter) != 0 {
		t.Errorf("ToolFilter = %v, want empty", got.ToolFilter)
	}
}
