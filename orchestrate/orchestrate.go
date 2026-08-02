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

package orchestrate

import (
	"context"
	"fmt"
	"time"

	"github.com/vogo/vage/schema"
)

// Runner executes a unit of work. agent.Agent satisfies this interface.
type Runner interface {
	Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
}

// ErrorStrategy controls how the DAG engine handles node failures.
type ErrorStrategy int

const (
	Abort ErrorStrategy = iota
	Skip
	Compensate
)

// InputMapFunc maps upstream results to the current node's input.
type InputMapFunc func(upstreamResults map[string]*schema.RunResponse) (*schema.RunRequest, error)

// Node is a single node in a DAG execution graph.
type Node struct {
	ID           string
	Runner       Runner
	Deps         []string
	InputMapper  InputMapFunc
	Optional     bool
	Condition    func(upstreamResults map[string]*schema.RunResponse) bool
	Timeout      time.Duration // Per-node execution timeout (0 = no limit).
	Retries      int           // Max retry count on failure (0 = no retry).
	ResourceTags []string      // Resource tags for concurrency/rate control.
	Priority     int           // Scheduling priority (higher = more priority).
}

// LoopNode defines a loop with a body runner and termination conditions.
type LoopNode struct {
	Body            Runner
	Condition       func(*schema.RunResponse) bool
	MaxIters        int
	ConvergenceFunc func(prev, curr *schema.RunResponse) bool
}

// DAGEventHandler receives lifecycle events during DAG execution.
// All methods must be safe for concurrent use.
type DAGEventHandler interface {
	OnNodeStart(nodeID string)
	OnNodeComplete(nodeID string, status NodeStatus, err error)
	OnCheckpointError(nodeID string, err error)
}

// DAGConfig holds configuration for DAG execution.
type DAGConfig struct {
	MaxConcurrency     int
	ErrorStrategy      ErrorStrategy
	EarlyExitFunc      func(nodeID string, resp *schema.RunResponse) bool
	Aggregator         Aggregator
	CheckpointStore    CheckpointStore     // Optional checkpoint store for save/resume.
	ReplayMode         bool                // When true, replay from checkpoint without executing runners.
	PriorityScheduling bool                // Use priority queue for ready nodes (default: FIFO).
	CriticalPathAuto   bool                // Auto-compute critical path priorities (requires PriorityScheduling).
	BackpressureCfg    *BackpressureConfig // Adaptive concurrency control (nil = disabled).
	ResourceLimits     map[string]int      // Per-resource-tag concurrency limits.
	ResourceRateLimits map[string]float64  // Per-resource-tag rate limits (requests/second).
	CompensateCfg      *CompensateConfig   // Compensation configuration (nil = disabled).
	EventHandler       DAGEventHandler     // Optional event handler for observability (nil = disabled).
}

// DAGOption is a functional option for configuring DAG execution.
type DAGOption func(*DAGConfig)

// WithMaxConcurrency sets the maximum number of concurrently running nodes.
func WithMaxConcurrency(n int) DAGOption {
	return func(c *DAGConfig) { c.MaxConcurrency = n }
}

// WithErrorStrategy sets the error handling strategy.
func WithErrorStrategy(s ErrorStrategy) DAGOption {
	return func(c *DAGConfig) { c.ErrorStrategy = s }
}

// WithEarlyExit sets a function that can trigger early DAG termination.
func WithEarlyExit(fn func(nodeID string, resp *schema.RunResponse) bool) DAGOption {
	return func(c *DAGConfig) { c.EarlyExitFunc = fn }
}

// WithAggregator sets the aggregator for combining terminal node results.
func WithAggregator(a Aggregator) DAGOption {
	return func(c *DAGConfig) { c.Aggregator = a }
}

// WithCheckpointStore enables checkpoint-based save/resume.
func WithCheckpointStore(cs CheckpointStore) DAGOption {
	return func(c *DAGConfig) { c.CheckpointStore = cs }
}

// WithReplayMode enables replaying from checkpoints without re-executing runners.
func WithReplayMode() DAGOption {
	return func(c *DAGConfig) { c.ReplayMode = true }
}

// WithPriorityScheduling enables priority-based scheduling with optional critical path auto-computation.
func WithPriorityScheduling(criticalPathAuto bool) DAGOption {
	return func(c *DAGConfig) {
		c.PriorityScheduling = true
		c.CriticalPathAuto = criticalPathAuto
	}
}

// WithBackpressure enables adaptive concurrency control.
func WithBackpressure(cfg *BackpressureConfig) DAGOption {
	return func(c *DAGConfig) { c.BackpressureCfg = cfg }
}

// WithResourceLimits sets per-resource-tag concurrency limits.
func WithResourceLimits(limits map[string]int) DAGOption {
	return func(c *DAGConfig) { c.ResourceLimits = limits }
}

// WithResourceRateLimits sets per-resource-tag rate limits (requests/second).
func WithResourceRateLimits(limits map[string]float64) DAGOption {
	return func(c *DAGConfig) { c.ResourceRateLimits = limits }
}

// WithCompensation enables compensation (Saga pattern) on failure.
func WithCompensation(cfg *CompensateConfig) DAGOption {
	return func(c *DAGConfig) {
		c.ErrorStrategy = Compensate
		c.CompensateCfg = cfg
	}
}

// WithEventHandler sets the event handler for observability.
func WithEventHandler(h DAGEventHandler) DAGOption {
	return func(c *DAGConfig) { c.EventHandler = h }
}

// RunDAG is a convenience entry point that builds a DAGConfig from functional options
// and delegates to ExecuteDAG. For advanced use cases, use ExecuteDAG directly.
func RunDAG(ctx context.Context, nodes []Node, req *schema.RunRequest, opts ...DAGOption) (*DAGResult, error) {
	var cfg DAGConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return ExecuteDAG(ctx, cfg, nodes, req)
}

// NodeStatus represents the execution status of a node.
type NodeStatus int

const (
	NodePending NodeStatus = iota
	NodeRunning
	NodeDone
	NodeFailed
	NodeSkipped
	NodeCompensated
)

// DAGResult holds the results of a DAG execution.
type DAGResult struct {
	NodeResults map[string]*schema.RunResponse
	NodeStatus  map[string]NodeStatus
	FinalOutput *schema.RunResponse
	Usage       *schema.Usage
	Timeline    []NodeTimeline // Node execution timeline (Gantt chart data).
}

// Edge represents a dependency edge from one node to another.
type Edge struct {
	From string
	To   string
}

// BuildDAG converts a list of nodes and edges into nodes with Deps populated.
// It validates that all edge references exist and that no node has pre-existing Deps
// (to avoid mixing the two definition styles).
func BuildDAG(nodes []Node, edges []Edge) ([]Node, error) {
	nodeIndex := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if _, exists := nodeIndex[n.ID]; exists {
			return nil, fmt.Errorf("orchestrate: duplicate node ID %q", n.ID)
		}
		if len(n.Deps) > 0 {
			return nil, fmt.Errorf("orchestrate: node %q has Deps set; cannot mix Deps and Edge styles", n.ID)
		}
		nodeIndex[n.ID] = i
	}

	// Build deps from edges.
	depsMap := make(map[string][]string, len(nodes))
	for _, e := range edges {
		if _, ok := nodeIndex[e.From]; !ok {
			return nil, fmt.Errorf("orchestrate: edge references unknown node %q (From)", e.From)
		}
		if _, ok := nodeIndex[e.To]; !ok {
			return nil, fmt.Errorf("orchestrate: edge references unknown node %q (To)", e.To)
		}
		depsMap[e.To] = append(depsMap[e.To], e.From)
	}

	// Copy nodes with Deps filled in.
	result := make([]Node, len(nodes))
	copy(result, nodes)
	for i := range result {
		if deps, ok := depsMap[result[i].ID]; ok {
			result[i].Deps = deps
		}
	}

	if err := ValidateDAG(result); err != nil {
		return nil, err
	}

	return result, nil
}

// ValidateDAG performs comprehensive validation on DAG nodes:
// duplicate IDs, missing dependencies, cycle detection, and connectivity check.
// Multiple root nodes (no deps) and multiple terminal nodes are allowed,
// but all nodes must be part of a single connected graph.
func ValidateDAG(nodes []Node) error {
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if seen[n.ID] {
			return fmt.Errorf("orchestrate: duplicate node ID %q", n.ID)
		}
		seen[n.ID] = true
	}

	for _, n := range nodes {
		for _, dep := range n.Deps {
			if !seen[dep] {
				return fmt.Errorf("orchestrate: node %q depends on unknown node %q", n.ID, dep)
			}
		}
	}

	if err := detectCycle(nodes); err != nil {
		return err
	}

	if err := checkConnected(nodes); err != nil {
		return err
	}

	return nil
}

// checkConnected verifies that all nodes form a single connected component
// when edges are treated as undirected. Returns an error if the graph is disconnected.
func checkConnected(nodes []Node) error {
	if len(nodes) <= 1 {
		return nil
	}

	// Build undirected adjacency list.
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		for _, dep := range n.Deps {
			adj[n.ID] = append(adj[n.ID], dep)
			adj[dep] = append(adj[dep], n.ID)
		}
	}

	// BFS from the first node.
	visited := make(map[string]bool, len(nodes))
	queue := []string{nodes[0].ID}
	visited[nodes[0].ID] = true

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, neighbor := range adj[cur] {
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}

	if len(visited) != len(nodes) {
		for _, n := range nodes {
			if !visited[n.ID] {
				return fmt.Errorf("orchestrate: node %q is disconnected from the rest of the graph", n.ID)
			}
		}
	}

	return nil
}
