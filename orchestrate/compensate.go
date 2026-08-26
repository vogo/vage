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
	"slices"
	"time"

	"github.com/vogo/vage/schema"
)

// Compensatable is implemented by Runners that support compensation (rollback).
type Compensatable interface {
	// Compensate rolls back the effects of a previously successful execution.
	Compensate(ctx context.Context, original *schema.RunResponse) error
	// Idempotent returns true if the Compensate operation is idempotent (safe to retry).
	// Deprecated: implement IdempotentChecker instead for new code.
	Idempotent() bool
}

// IdempotentChecker is implemented by operations that can indicate whether they are idempotent.
// This is checked via type assertion and can be implemented by any Runner or Compensatable.
type IdempotentChecker interface {
	Idempotent() bool
}

// CompensateStrategy defines the compensation approach.
type CompensateStrategy int

const (
	// BackwardCompensate rolls back completed nodes in reverse topological order (Saga pattern).
	BackwardCompensate CompensateStrategy = iota
	// ForwardRecovery retries the failed node until success or max retries.
	ForwardRecovery
)

// CompensateConfig configures compensation behavior.
type CompensateConfig struct {
	Strategy   CompensateStrategy // Compensation approach.
	Timeout    time.Duration      // Timeout for each individual compensation operation.
	MaxRetries int                // Max retries for compensation (only if Idempotent).
}

// executeBackwardCompensation performs backward compensation (Saga rollback).
// It compensates completed nodes in reverse topological order.
func executeBackwardCompensation(ctx context.Context, cfg *CompensateConfig,
	nodes []Node, result *DAGResult,
) error {
	// Get completed nodes in topological order.
	topoOrder := topologicalSort(nodes)

	// Reverse the order for backward compensation.
	var completedReverse []string
	for _, id := range slices.Backward(topoOrder) {
		if result.NodeStatus[id] == NodeDone {
			completedReverse = append(completedReverse, id)
		}
	}

	nodeMap := make(map[string]*Node, len(nodes))
	for i := range nodes {
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	var compensationErrors []error
	for _, nodeID := range completedReverse {
		node := nodeMap[nodeID]
		if node == nil || node.Runner == nil {
			continue
		}

		comp, ok := node.Runner.(Compensatable)
		if !ok {
			continue
		}

		resp := result.NodeResults[nodeID]
		if resp == nil {
			continue
		}

		err := executeNodeCompensation(ctx, cfg, nodeID, comp, resp)
		if err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("node %q: %w", nodeID, err))
		} else {
			result.NodeStatus[nodeID] = NodeCompensated
		}
	}

	if len(compensationErrors) > 0 {
		return fmt.Errorf("orchestrate: compensation errors: %v", compensationErrors)
	}
	return nil
}

// executeNodeCompensation compensates a single node with timeout and retry support.
func executeNodeCompensation(ctx context.Context, cfg *CompensateConfig,
	nodeID string, comp Compensatable, resp *schema.RunResponse,
) error {
	maxAttempts := 1
	idempotent := false
	if ic, ok := comp.(IdempotentChecker); ok {
		idempotent = ic.Idempotent()
	}
	if idempotent && cfg.MaxRetries > 0 {
		maxAttempts = cfg.MaxRetries + 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := compensateWithTimeout(ctx, cfg.Timeout, comp, resp)
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt < maxAttempts-1 {
			// Exponential backoff for retries.
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return fmt.Errorf("orchestrate: compensation for node %q failed after %d attempts: %w",
		nodeID, maxAttempts, lastErr)
}

// executeForwardRecovery retries the failed node until success or max retries.
func executeForwardRecovery(ctx context.Context, cfg *CompensateConfig,
	node *Node, req *schema.RunRequest,
) (*schema.RunResponse, error) {
	maxAttempts := cfg.MaxRetries + 1
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := runRunnerWithTimeout(ctx, cfg.Timeout, node.Runner, req)
		if err == nil && resp != nil {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("nil response")
		}

		if attempt < maxAttempts-1 {
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return nil, fmt.Errorf("orchestrate: forward recovery for node %q failed after %d attempts: %w",
		node.ID, maxAttempts, lastErr)
}

// compensateWithTimeout runs compensation with an optional timeout.
func compensateWithTimeout(ctx context.Context, timeout time.Duration,
	comp Compensatable, resp *schema.RunResponse,
) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return comp.Compensate(ctx, resp)
}

// runRunnerWithTimeout runs a Runner with an optional timeout.
func runRunnerWithTimeout(ctx context.Context, timeout time.Duration,
	runner Runner, req *schema.RunRequest,
) (*schema.RunResponse, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return runner.Run(ctx, req)
}
