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
	"fmt"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// Route pairs an Agent with a description used for routing decisions.
type Route struct {
	Agent       agent.Agent
	Description string
}

// RouteResult holds the result of a routing decision.
type RouteResult struct {
	Agent agent.Agent
	Usage *aimodel.Usage // optional usage from the routing decision itself
}

// RouteFunc selects which agent to route a request to.
type RouteFunc func(ctx context.Context, req *schema.RunRequest, routes []Route) (*RouteResult, error)

// Agent routes requests to one of several sub-agents based on a RouteFunc.
type Agent struct {
	agent.Base
	routes    []Route
	routeFunc RouteFunc
}

var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.StreamAgent = (*Agent)(nil)
)

// Option configures a router Agent.
type Option func(*Agent)

// WithFunc sets the routing function for a router Agent.
func WithFunc(fn RouteFunc) Option {
	return func(a *Agent) { a.routeFunc = fn }
}

// New creates a router Agent with the given routes and options.
func New(cfg agent.Config, routes []Route, opts ...Option) *Agent {
	for i, r := range routes {
		if r.Agent == nil {
			panic(fmt.Sprintf("routeragent: route[%d] has nil Agent", i))
		}
	}

	a := &Agent{
		Base:   agent.NewBase(cfg),
		routes: routes,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Run selects one sub-agent via the routing function and delegates execution to it.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	start := time.Now()

	if len(a.routes) == 0 {
		return nil, fmt.Errorf("routeragent: no routes configured")
	}

	if a.routeFunc == nil {
		return nil, fmt.Errorf("routeragent: no routing function")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := a.routeFunc(ctx, req, a.routes)
	if err != nil {
		return nil, fmt.Errorf("routeragent: route select: %w", err)
	}

	if result == nil || result.Agent == nil {
		return nil, fmt.Errorf("routeragent: route select returned nil agent")
	}

	resp, err := result.Agent.Run(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return nil, fmt.Errorf("routeragent: nil response from agent %s", result.Agent.ID())
	}

	// Aggregate usage from routing decision and selected agent.
	if result.Usage != nil {
		totalUsage := &aimodel.Usage{}
		totalUsage.Add(result.Usage)
		if resp.Usage != nil {
			totalUsage.Add(resp.Usage)
		}
		resp.Usage = totalUsage
	}

	resp.SessionID = req.SessionID
	resp.Duration = time.Since(start).Milliseconds()

	return resp, nil
}

// RunStream returns a RunStream that emits lifecycle events as the agent executes.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	return agent.RunToStream(ctx, a, req), nil
}
