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

package routeragent

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

func TestAgent_Config(t *testing.T) {
	routes := []Route{
		{Agent: agent.NewCustomAgent(agent.Config{ID: "sub-1"}, nil), Description: "route one"},
	}
	a := New(agent.Config{ID: "rt-1", Name: "router"}, routes)
	if a.ID() != "rt-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "rt-1")
	}
	if a.Name() != "router" {
		t.Errorf("Name = %q, want %q", a.Name(), "router")
	}
}

func TestAgent_WithFunc(t *testing.T) {
	fn := func(_ context.Context, _ *schema.RunRequest, _ []Route) (agent.Agent, error) {
		return nil, nil
	}
	a := New(agent.Config{}, nil, WithFunc(fn))
	if a.routerFunc == nil {
		t.Error("routerFunc should not be nil")
	}
}

func TestAgent_Run_Stub(t *testing.T) {
	a := New(agent.Config{}, nil)
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %q, want 'not yet implemented'", err.Error())
	}
}
