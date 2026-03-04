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

package dagagent

import (
	"context"
	"errors"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// Node is a single node in a DAG execution graph.
type Node struct {
	ID    string      // unique identifier for this node
	Agent agent.Agent // the agent to execute
	Deps  []string    // IDs of nodes this node depends on
}

// Agent executes a directed acyclic graph of sub-agents.
type Agent struct {
	agent.Base
	nodes []Node
}

var _ agent.Agent = (*Agent)(nil)

// New creates a DAG Agent with the given execution nodes.
func New(cfg agent.Config, nodes []Node) *Agent {
	return &Agent{
		Base:  agent.NewBase(cfg),
		nodes: nodes,
	}
}

// Run is not yet implemented.
func (a *Agent) Run(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
	return nil, errors.New("vagent: dag.Agent.Run not yet implemented")
}
