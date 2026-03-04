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
	"errors"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// Route pairs an Agent with a description used for routing decisions.
type Route struct {
	Agent       agent.Agent
	Description string
}

// Func selects which agent to route a request to.
type Func func(ctx context.Context, req *schema.RunRequest, routes []Route) (agent.Agent, error)

// Agent routes requests to one of several sub-agents based on a Func.
type Agent struct {
	agent.Base
	routes     []Route
	routerFunc Func
}

var _ agent.Agent = (*Agent)(nil)

// Option configures a router Agent.
type Option func(*Agent)

// WithFunc sets the routing function for a router Agent.
func WithFunc(fn Func) Option {
	return func(a *Agent) { a.routerFunc = fn }
}

// New creates a router Agent with the given routes and options.
func New(cfg agent.Config, routes []Route, opts ...Option) *Agent {
	a := &Agent{
		Base:   agent.NewBase(cfg),
		routes: routes,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Run is not yet implemented.
func (a *Agent) Run(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
	return nil, errors.New("vagent: router.Agent.Run not yet implemented")
}
