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

package toolkit

import (
	"fmt"
	"sync"
	"testing"
)

func TestMemoryReadTracker_HasRead_Unrecorded(t *testing.T) {
	tracker := NewMemoryReadTracker(0)

	if tracker.HasRead("/some/path.txt") {
		t.Error("expected HasRead to return false for unrecorded path")
	}
}

func TestMemoryReadTracker_HasRead_AfterRecord(t *testing.T) {
	tracker := NewMemoryReadTracker(0)

	tracker.RecordRead("/some/path.txt")

	if !tracker.HasRead("/some/path.txt") {
		t.Error("expected HasRead to return true after RecordRead")
	}
}

func TestMemoryReadTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewMemoryReadTracker(0)

	const n = 100

	var wg sync.WaitGroup

	// Concurrently record and read paths.
	for i := range n {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			path := fmt.Sprintf("/path/%d.txt", idx)
			tracker.RecordRead(path)

			if !tracker.HasRead(path) {
				t.Errorf("expected HasRead to return true for %s", path)
			}
		}(i)
	}

	wg.Wait()
}

func TestMemoryReadTracker_Eviction(t *testing.T) {
	tracker := NewMemoryReadTracker(3)

	tracker.RecordRead("/a.txt")
	tracker.RecordRead("/b.txt")
	tracker.RecordRead("/c.txt")

	// Map is full (3 entries). Next RecordRead should clear and add only the new one.
	tracker.RecordRead("/d.txt")

	if tracker.HasRead("/a.txt") {
		t.Error("expected /a.txt to be evicted")
	}

	if tracker.HasRead("/b.txt") {
		t.Error("expected /b.txt to be evicted")
	}

	if tracker.HasRead("/c.txt") {
		t.Error("expected /c.txt to be evicted")
	}

	if !tracker.HasRead("/d.txt") {
		t.Error("expected /d.txt to be present after eviction")
	}
}

func TestMemoryReadTracker_UnlimitedEntries(t *testing.T) {
	tracker := NewMemoryReadTracker(0)

	for i := range 1000 {
		tracker.RecordRead(fmt.Sprintf("/path/%d.txt", i))
	}

	// All should still be present with unlimited (0) maxEntries.
	for i := range 1000 {
		path := fmt.Sprintf("/path/%d.txt", i)
		if !tracker.HasRead(path) {
			t.Errorf("expected HasRead to return true for %s", path)
		}
	}
}
