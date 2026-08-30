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

	"github.com/vogo/vage/schema"
)

// Branch represents a conditional branch with a condition function and target node ID.
type Branch struct {
	// Condition evaluates upstream results and returns true if this branch should be taken.
	Condition func(upstreamResults map[string]*schema.RunResponse) bool
	// TargetID is the ID of the node to activate when this branch is taken.
	TargetID string
}

// ConditionalNode represents a node that routes execution to different branches
// based on upstream results. Branches are evaluated in order; the first match wins.
type ConditionalNode struct {
	Node                // Embedded base node (Runner executes first, then branches are evaluated).
	Branches   []Branch // Conditional branches, evaluated in order.
	Default    string   // Default target node ID when no branch matches (empty = skip).
	Exhaustive bool     // When true, validation requires Default to be non-empty.
}

// ValidateConditionalNode validates a ConditionalNode's configuration.
func ValidateConditionalNode(cn *ConditionalNode) error {
	if cn.Exhaustive && cn.Default == "" {
		return fmt.Errorf("orchestrate: ConditionalNode %q has Exhaustive=true but no Default branch", cn.ID)
	}
	for i, b := range cn.Branches {
		if b.Condition == nil {
			return fmt.Errorf("orchestrate: ConditionalNode %q branch %d has nil Condition", cn.ID, i)
		}
		if b.TargetID == "" {
			return fmt.Errorf("orchestrate: ConditionalNode %q branch %d has empty TargetID", cn.ID, i)
		}
	}
	return nil
}

// EvaluateBranches evaluates the conditional branches and returns the target node ID.
// Returns empty string if no branch matches and no default is set.
func (cn *ConditionalNode) EvaluateBranches(upstreamResults map[string]*schema.RunResponse) string {
	for _, b := range cn.Branches {
		if b.Condition(upstreamResults) {
			return b.TargetID
		}
	}
	return cn.Default
}

// ExecuteConditional runs a ConditionalNode: first executes the node's runner,
// then evaluates branches to determine which target nodes to run.
// It returns the runner's response and the selected target node ID.
func ExecuteConditional(ctx context.Context, cn *ConditionalNode, req *schema.RunRequest,
	upstreamResults map[string]*schema.RunResponse,
) (*schema.RunResponse, string, error) {
	if err := ValidateConditionalNode(cn); err != nil {
		return nil, "", err
	}

	var resp *schema.RunResponse
	if cn.Runner != nil {
		var err error
		resp, err = cn.Runner.Run(ctx, req)
		if err != nil {
			return nil, "", fmt.Errorf("orchestrate: ConditionalNode %q runner failed: %w", cn.ID, err)
		}

		// Branch selection reads upstream results, but a suspended runner still
		// means this node produced no output — do not let it pick a target.
		if rejectErr := rejectInterrupted(fmt.Sprintf("ConditionalNode %q runner", cn.ID), resp); rejectErr != nil {
			return nil, "", rejectErr
		}
	}

	targetID := cn.EvaluateBranches(upstreamResults)
	return resp, targetID, nil
}
