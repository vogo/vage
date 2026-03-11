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
	"sync"
	"time"

	"github.com/vogo/vagent/schema"
)

type spawnDepthKey struct{}

// DynamicSpawnNode generates child nodes at runtime from the parent's output (Map-Reduce pattern).
type DynamicSpawnNode struct {
	Node                                                                                  // Embedded base node.
	Spawner         func(ctx context.Context, output *schema.RunResponse) ([]Node, error) // Generates child nodes.
	SpawnAggregator Aggregator                                                            // Aggregates child results.
	MaxSpawnCount   int                                                                   // Max number of spawned nodes (0 = unlimited).
	SpawnTimeout    time.Duration                                                         // Timeout for all spawned nodes (0 = no timeout).
	SpawnDepthLimit int                                                                   // Max recursion depth for nested spawns (0 = no nesting).
}

// ExecuteDynamicSpawn executes a DynamicSpawnNode:
// 1. Runs the node's own runner to produce output.
// 2. Calls Spawner to generate child nodes.
// 3. Executes child nodes in parallel.
// 4. Aggregates results using SpawnAggregator.
func ExecuteDynamicSpawn(ctx context.Context, dsn *DynamicSpawnNode, req *schema.RunRequest) (*schema.RunResponse, error) {
	if dsn.Spawner == nil {
		return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q has nil Spawner", dsn.ID)
	}

	// Check spawn depth.
	currentDepth := 0
	if d, ok := ctx.Value(spawnDepthKey{}).(int); ok {
		currentDepth = d
	}
	if dsn.SpawnDepthLimit > 0 && currentDepth >= dsn.SpawnDepthLimit {
		return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q exceeded SpawnDepthLimit (%d)", dsn.ID, dsn.SpawnDepthLimit)
	}

	// Run the node's own runner first.
	var output *schema.RunResponse
	if dsn.Runner != nil {
		var err error
		output, err = dsn.Runner.Run(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q runner failed: %w", dsn.ID, err)
		}
	} else {
		output = &schema.RunResponse{Messages: req.Messages, SessionID: req.SessionID}
	}

	// Spawn child nodes with a cancellable context so that if one child fails,
	// other children are cancelled promptly to avoid goroutine leaks.
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()
	childCtx = context.WithValue(childCtx, spawnDepthKey{}, currentDepth+1)
	if dsn.SpawnTimeout > 0 {
		var timeoutCancel context.CancelFunc
		childCtx, timeoutCancel = context.WithTimeout(childCtx, dsn.SpawnTimeout)
		defer timeoutCancel()
	}

	children, err := dsn.Spawner(childCtx, output)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q Spawner failed: %w", dsn.ID, err)
	}

	if dsn.MaxSpawnCount > 0 && len(children) > dsn.MaxSpawnCount {
		return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q spawned %d nodes, exceeds MaxSpawnCount %d",
			dsn.ID, len(children), dsn.MaxSpawnCount)
	}

	if len(children) == 0 {
		return output, nil
	}

	// Execute children in parallel.
	type childResult struct {
		id   string
		resp *schema.RunResponse
		err  error
	}

	resultCh := make(chan childResult, len(children))
	var wg sync.WaitGroup

	for i := range children {
		child := &children[i]
		wg.Go(func() {
			childReq := &schema.RunRequest{
				Messages:  output.Messages,
				SessionID: req.SessionID,
				Options:   req.Options,
				Metadata:  req.Metadata,
			}
			resp, err := child.Runner.Run(childCtx, childReq)
			resultCh <- childResult{id: child.ID, resp: resp, err: err}
		})
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	childResults := make(map[string]*schema.RunResponse, len(children))
	for cr := range resultCh {
		if cr.err != nil {
			return nil, fmt.Errorf("orchestrate: DynamicSpawnNode %q child %q failed: %w", dsn.ID, cr.id, cr.err)
		}
		childResults[cr.id] = cr.resp
	}

	// Aggregate results.
	agg := dsn.SpawnAggregator
	if agg == nil {
		agg = ConcatMessagesAggregator()
	}
	return agg.Aggregate(ctx, childResults)
}
