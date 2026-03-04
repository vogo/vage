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

package agent

import (
	"context"
	"errors"

	"github.com/vogo/vagent/schema"
)

// Route pairs an Agent with a description used for routing decisions.
type Route struct {
	Agent       Agent
	Description string
}

// RouterFunc selects which agent to route a request to.
type RouterFunc func(ctx context.Context, req *schema.RunRequest, routes []Route) (Agent, error)

// RouterAgent routes requests to one of several sub-agents based on a RouterFunc.
type RouterAgent struct {
	agentMeta
	routes     []Route
	routerFunc RouterFunc
}

var _ Agent = (*RouterAgent)(nil)

// RouterOption configures a RouterAgent.
type RouterOption func(*RouterAgent)

// WithRouterFunc sets the routing function for a RouterAgent.
func WithRouterFunc(fn RouterFunc) RouterOption {
	return func(a *RouterAgent) { a.routerFunc = fn }
}

// NewRouterAgent creates a RouterAgent with the given routes and options.
func NewRouterAgent(cfg Config, routes []Route, opts ...RouterOption) *RouterAgent {
	a := &RouterAgent{
		agentMeta: newAgentMeta(cfg),
		routes:    routes,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Run is not yet implemented.
func (a *RouterAgent) Run(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
	return nil, errors.New("vagent: RouterAgent.Run not yet implemented")
}
