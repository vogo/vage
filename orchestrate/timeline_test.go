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

package orchestrate

import (
	"context"
	"testing"
	"time"

	"github.com/vogo/largemodel/schema"
)

func TestTimelineTracker_Basic(t *testing.T) {
	tracker := newTimelineTracker()

	tracker.recordStart("A")
	time.Sleep(10 * time.Millisecond)
	tracker.recordEnd("A", NodeDone)

	tracker.recordStart("B")
	time.Sleep(10 * time.Millisecond)
	tracker.recordEnd("B", NodeFailed)

	result := tracker.result()
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}

	// Should be sorted by start time (A first).
	if result[0].NodeID != "A" {
		t.Errorf("first entry should be A, got %q", result[0].NodeID)
	}
	if result[1].NodeID != "B" {
		t.Errorf("second entry should be B, got %q", result[1].NodeID)
	}

	// Check statuses.
	if result[0].Status != NodeDone {
		t.Errorf("A status = %d, want NodeDone", result[0].Status)
	}
	if result[1].Status != NodeFailed {
		t.Errorf("B status = %d, want NodeFailed", result[1].Status)
	}

	// Check durations are positive.
	if result[0].Duration <= 0 {
		t.Errorf("A duration should be positive, got %v", result[0].Duration)
	}
	if result[1].Duration <= 0 {
		t.Errorf("B duration should be positive, got %v", result[1].Duration)
	}

	// Check start < end.
	if !result[0].StartTime.Before(result[0].EndTime) {
		t.Error("A start should be before end")
	}
}

func TestTimelineTracker_Empty(t *testing.T) {
	tracker := newTimelineTracker()
	result := tracker.result()
	if len(result) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result))
	}
}

func TestDAG_TimelinePopulated(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			time.Sleep(10 * time.Millisecond)
			return &schema.RunResponse{Messages: req.Messages}, nil
		})},
		{ID: "B", Runner: passthroughRunner(), Deps: []string{"A"}},
	}

	result, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Timeline) != 2 {
		t.Fatalf("expected 2 timeline entries, got %d", len(result.Timeline))
	}

	// Both should be NodeDone.
	for _, tl := range result.Timeline {
		if tl.Status != NodeDone {
			t.Errorf("node %s timeline status = %d, want NodeDone", tl.NodeID, tl.Status)
		}
		if tl.Duration <= 0 {
			t.Errorf("node %s duration should be positive", tl.NodeID)
		}
	}

	// A should start before B.
	aTimeline := result.Timeline[0]
	bTimeline := result.Timeline[1]
	if aTimeline.NodeID != "A" {
		// Swap if sorted differently.
		aTimeline, bTimeline = bTimeline, aTimeline
	}
	if !aTimeline.StartTime.Before(bTimeline.StartTime) {
		t.Error("A should start before B")
	}
}

func TestDAG_TimelineWithSkippedNode(t *testing.T) {
	nodes := []Node{
		{ID: "A", Runner: appendRunner("-A")},
		{
			ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"},
			Condition: func(_ map[string]*schema.RunResponse) bool { return false },
		},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
	}

	result, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Timeline should include skipped node B.
	foundSkipped := false
	for _, tl := range result.Timeline {
		if tl.NodeID == "B" && tl.Status == NodeSkipped {
			foundSkipped = true
		}
	}
	if !foundSkipped {
		t.Error("expected skipped node B in timeline")
	}
}
