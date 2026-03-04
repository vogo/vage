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

// Node is a single node in a DAG execution graph.
type Node struct {
	ID    string   // unique identifier for this node
	Agent Agent    // the agent to execute
	Deps  []string // IDs of nodes this node depends on
}

// DAGAgent executes a directed acyclic graph of sub-agents.
type DAGAgent struct {
	agentMeta
	nodes []Node
}

var _ Agent = (*DAGAgent)(nil)

// NewDAGAgent creates a DAGAgent with the given execution nodes.
func NewDAGAgent(cfg Config, nodes []Node) *DAGAgent {
	return &DAGAgent{
		agentMeta: newAgentMeta(cfg),
		nodes:     nodes,
	}
}

// Run is not yet implemented.
func (a *DAGAgent) Run(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
	return nil, errors.New("vagent: DAGAgent.Run not yet implemented")
}
