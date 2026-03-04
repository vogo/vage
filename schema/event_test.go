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

package schema

import (
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	before := time.Now()
	e := NewEvent(EventTextDelta, "agent-1", "sess-1", TextDeltaData{Delta: "hi"})
	after := time.Now()

	if e.Type != EventTextDelta {
		t.Errorf("Type = %q, want %q", e.Type, EventTextDelta)
	}
	if e.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "agent-1")
	}
	if e.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "sess-1")
	}
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Errorf("Timestamp %v not in range [%v, %v]", e.Timestamp, before, after)
	}

	data, ok := e.Data.(TextDeltaData)
	if !ok {
		t.Fatalf("Data type = %T, want TextDeltaData", e.Data)
	}
	if data.Delta != "hi" {
		t.Errorf("Delta = %q, want %q", data.Delta, "hi")
	}
}

func TestNewEvent_AllTypes(t *testing.T) {
	types := []string{
		EventAgentStart, EventTextDelta, EventToolCallStart,
		EventToolCallEnd, EventToolResult, EventIterationStart,
		EventAgentEnd, EventError,
	}
	for _, et := range types {
		e := NewEvent(et, "", "", nil)
		if e.Type != et {
			t.Errorf("Type = %q, want %q", e.Type, et)
		}
	}
}

func TestNewEvent_NilData(t *testing.T) {
	e := NewEvent(EventAgentStart, "a", "s", nil)
	if e.Data != nil {
		t.Errorf("Data = %v, want nil", e.Data)
	}
}

func TestEventData_SealedInterface(t *testing.T) {
	// Verify all data types implement EventData.
	var _ EventData = AgentStartData{}
	var _ EventData = TextDeltaData{}
	var _ EventData = ToolCallStartData{}
	var _ EventData = ToolCallEndData{}
	var _ EventData = ToolResultData{}
	var _ EventData = IterationStartData{}
	var _ EventData = AgentEndData{}
	var _ EventData = ErrorData{}
}
