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

package context_tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// TestBuildReport_JSONRoundTrip covers AC-2.3: a populated BuildReport
// produced by a real Build run must round-trip through json.Marshal and
// json.Unmarshal without errors and without losing any of the
// observable fields.
func TestBuildReport_JSONRoundTrip(t *testing.T) {
	sess := memory.NewSessionMemory("json", "json-session")
	ctx := context.Background()
	for i := range 3 {
		key := fmt.Sprintf("msg:%06d", i)
		text := fmt.Sprintf("turn-%d", i)
		if err := sess.Set(ctx, key, schema.NewUserMessage(text), 0); err != nil {
			t.Fatalf("seed Set %d: %v", i, err)
		}
	}
	mm := memory.NewManager(memory.WithSession(sess))

	builder := vctx.NewDefaultBuilder(
		vctx.WithSource(&vctx.SystemPromptSource{Template: prompt.StringPrompt("Sys.")}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: mm}),
		vctx.WithSource(&vctx.RequestMessagesSource{}),
	)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: "json-session",
		Request: &schema.RunRequest{
			SessionID: "json-session",
			Messages:  []schema.Message{schema.NewUserMessage("now")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	original := res.Report
	bytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal BuildReport: %v", err)
	}

	var decoded vctx.BuildReport
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("Unmarshal BuildReport: %v", err)
	}

	if decoded.BuilderName != original.BuilderName {
		t.Errorf("BuilderName: decoded=%q original=%q", decoded.BuilderName, original.BuilderName)
	}
	if decoded.Strategy != original.Strategy {
		t.Errorf("Strategy: decoded=%q original=%q", decoded.Strategy, original.Strategy)
	}
	if decoded.OutputCount != original.OutputCount {
		t.Errorf("OutputCount: decoded=%d original=%d", decoded.OutputCount, original.OutputCount)
	}
	if decoded.OutputTokens != original.OutputTokens {
		t.Errorf("OutputTokens: decoded=%d original=%d", decoded.OutputTokens, original.OutputTokens)
	}
	if len(decoded.Sources) != len(original.Sources) {
		t.Errorf("Sources length: decoded=%d original=%d", len(decoded.Sources), len(original.Sources))
	}

	// JSON must use the documented field names.
	jsonStr := string(bytes)
	for _, want := range []string{`"builder"`, `"strategy"`, `"output_count"`, `"sources"`} {
		if !strings.Contains(jsonStr, want) {
			t.Errorf("JSON missing field %s; got: %s", want, jsonStr)
		}
	}

	// And the event-data conversion must also serialize cleanly.
	evtData := original.ToEventData()
	bytes2, err := json.Marshal(evtData)
	if err != nil {
		t.Fatalf("Marshal ContextBuiltData: %v", err)
	}
	var evtDecoded schema.ContextBuiltData
	if err := json.Unmarshal(bytes2, &evtDecoded); err != nil {
		t.Fatalf("Unmarshal ContextBuiltData: %v", err)
	}
	if evtDecoded.Builder != original.BuilderName {
		t.Errorf("event Builder = %q, want %q", evtDecoded.Builder, original.BuilderName)
	}
}
