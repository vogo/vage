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
)

func TestArchiveAll(t *testing.T) {
	a := ArchiveAll()
	ctx := context.Background()

	entries := []Entry{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
	}

	result, err := a.Archive(ctx, entries)
	if err != nil {
		t.Fatalf("Archive error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Archive len = %d, want 2", len(result))
	}
}

func TestArchiveNone(t *testing.T) {
	a := ArchiveNone()
	ctx := context.Background()

	entries := []Entry{
		{Key: "a", Value: "1"},
	}

	result, err := a.Archive(ctx, entries)
	if err != nil {
		t.Fatalf("Archive error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("Archive len = %d, want 0", len(result))
	}
}

func TestArchiveFunc(t *testing.T) {
	// Custom archiver that only archives entries with non-empty AgentID.
	a := ArchiveFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		var result []Entry
		for _, e := range entries {
			if e.AgentID != "" {
				result = append(result, e)
			}
		}
		return result, nil
	})

	ctx := context.Background()
	entries := []Entry{
		{Key: "a", Value: "1", AgentID: "agent-1"},
		{Key: "b", Value: "2"},
	}

	result, err := a.Archive(ctx, entries)
	if err != nil {
		t.Fatalf("Archive error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Archive len = %d, want 1", len(result))
	}
	if result[0].Key != "a" {
		t.Errorf("Archive result key = %q, want %q", result[0].Key, "a")
	}
}
