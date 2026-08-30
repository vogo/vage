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

package routeragent_tests

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/routeragent"
	"github.com/vogo/vage/integrations/internal/subagent"
	"github.com/vogo/vage/schema"
)

// TestRouter_SuspendedSubAgent_ReturnsError: the router forwards a request and
// hands the response back verbatim, so a suspended sub-agent would otherwise
// reach the caller as the route's answer with its session id and usage
// rewritten. It must fail instead — and must not silently try another route.
func TestRouter_SuspendedSubAgent_ReturnsError(t *testing.T) {
	var handlerRuns atomic.Int32
	var fallbackRuns atomic.Int32

	suspending := subagent.Suspending("hitl-agent", &handlerRuns)
	fallback := agent.NewCustomAgent(
		agent.Config{ID: "fallback-agent"},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			fallbackRuns.Add(1)

			return &schema.RunResponse{Messages: req.Messages}, nil
		},
	)

	router := routeragent.New(
		agent.Config{ID: "router"},
		[]routeragent.Route{
			{Agent: suspending, Description: "asks a human"},
			{Agent: fallback, Description: "never selected"},
		},
		routeragent.WithFunc(routeragent.IndexFunc(0)),
	)

	resp, err := router.Run(context.Background(), subagent.Request("sess-router", "please ask"))
	if err == nil {
		t.Fatalf("expected an error, got response %+v", resp)
	}
	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}

	for _, want := range []string{"hitl-agent", "suspended", "nested human-in-the-loop is not supported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	if fallbackRuns.Load() != 0 {
		t.Errorf("router fell through to another route %d times, want 0", fallbackRuns.Load())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("flagged tool handler ran %d times, want 0", handlerRuns.Load())
	}
}

// TestRouter_CompletingSubAgent_StillSucceeds is the control: the ordinary
// routing result, including the session id rewrite, is unchanged.
func TestRouter_CompletingSubAgent_StillSucceeds(t *testing.T) {
	router := routeragent.New(
		agent.Config{ID: "router"},
		[]routeragent.Route{{Agent: subagent.Completing("plain-agent", "routed answer"), Description: "answers"}},
		routeragent.WithFunc(routeragent.IndexFunc(0)),
	)

	resp, err := router.Run(context.Background(), subagent.Request("sess-router", "hello"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.SessionID != "sess-router" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-router")
	}
	if !strings.Contains(resp.Messages[len(resp.Messages)-1].Text(), "routed answer") {
		t.Errorf("response text = %q, want it to contain %q",
			resp.Messages[len(resp.Messages)-1].Text(), "routed answer")
	}
}
