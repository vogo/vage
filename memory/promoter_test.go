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

func TestPromoteAll(t *testing.T) {
	p := PromoteAll()
	ctx := context.Background()

	entries := []Entry{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
	}

	result, err := p.Promote(ctx, entries)
	if err != nil {
		t.Fatalf("Promote error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Promote len = %d, want 2", len(result))
	}
}

func TestPromoteNone(t *testing.T) {
	p := PromoteNone()
	ctx := context.Background()

	entries := []Entry{
		{Key: "a", Value: "1"},
	}

	result, err := p.Promote(ctx, entries)
	if err != nil {
		t.Fatalf("Promote error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("Promote len = %d, want 0", len(result))
	}
}

func TestPromoteFunc(t *testing.T) {
	// Custom promoter that only promotes entries with key prefix "msg:".
	p := PromoteFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		var result []Entry
		for _, e := range entries {
			if len(e.Key) > 4 && e.Key[:4] == "msg:" {
				result = append(result, e)
			}
		}
		return result, nil
	})

	ctx := context.Background()
	entries := []Entry{
		{Key: "msg:1", Value: "hello"},
		{Key: "meta:x", Value: "data"},
		{Key: "msg:2", Value: "world"},
	}

	result, err := p.Promote(ctx, entries)
	if err != nil {
		t.Fatalf("Promote error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Promote len = %d, want 2", len(result))
	}
}
