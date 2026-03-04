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

package hook

import (
	"context"
	"testing"

	"github.com/vogo/vagent/schema"
)

func TestHookFunc_OnEvent(t *testing.T) {
	var received schema.Event

	hf := NewHookFunc(func(_ context.Context, e schema.Event) error {
		received = e
		return nil
	})

	event := schema.NewEvent(schema.EventTextDelta, "a1", "s1", schema.TextDeltaData{Delta: "hi"})

	if err := hf.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("OnEvent() error = %v", err)
	}

	if received.Type != schema.EventTextDelta {
		t.Errorf("received.Type = %q, want %q", received.Type, schema.EventTextDelta)
	}

	if received.AgentID != "a1" {
		t.Errorf("received.AgentID = %q, want %q", received.AgentID, "a1")
	}
}

func TestHookFunc_FilterEmpty(t *testing.T) {
	hf := NewHookFunc(func(_ context.Context, _ schema.Event) error { return nil })

	if f := hf.Filter(); len(f) != 0 {
		t.Errorf("Filter() = %v, want empty", f)
	}
}

func TestHookFunc_FilterSpecific(t *testing.T) {
	hf := NewHookFunc(
		func(_ context.Context, _ schema.Event) error { return nil },
		schema.EventError, schema.EventAgentEnd,
	)

	f := hf.Filter()
	if len(f) != 2 {
		t.Fatalf("Filter() len = %d, want 2", len(f))
	}

	if f[0] != schema.EventError {
		t.Errorf("Filter()[0] = %q, want %q", f[0], schema.EventError)
	}

	if f[1] != schema.EventAgentEnd {
		t.Errorf("Filter()[1] = %q, want %q", f[1], schema.EventAgentEnd)
	}
}

func TestHookFunc_ImplementsHook(t *testing.T) {
	var _ Hook = HookFunc{}
}
