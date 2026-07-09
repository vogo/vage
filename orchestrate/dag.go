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

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// ExecuteDAG runs a DAG of nodes with the given config and initial request.
func ExecuteDAG(ctx context.Context, cfg DAGConfig, nodes []Node, req *schema.RunRequest) (*DAGResult, error) {
	if len(nodes) == 0 {
		return &DAGResult{
			NodeResults: map[string]*schema.RunResponse{},
			NodeStatus:  map[string]NodeStatus{},
			FinalOutput: &schema.RunResponse{Messages: req.Messages, SessionID: req.SessionID},
		}, nil
	}

	de, err := newDagExecutor(ctx, cfg, nodes, req)
	if err != nil {
		return nil, err
	}
	defer de.cancel()

	return de.run()
}

// dagExecutor holds all state for a single DAG execution.
type dagExecutor struct {
	cfg       DAGConfig
	nodes     []Node
	nodeMap   map[string]*Node
	req       *schema.RunRequest
	result    *DAGResult
	timeline  *timelineTracker
	resMgr    *resourceManager
	bpCtrl    *backpressureController
	sem       chan struct{}
	pq        *priorityQueue
	activePQ  int
	doneCh    chan nodeCompletion
	pending   int
	mu        sync.Mutex
	parentCtx context.Context
	ctx       context.Context
	cancel    context.CancelFunc

	downstream    map[string][]string
	inDegree      map[string]int
	terminalNodes map[string]bool

	totalUsage aimodel.Usage
	hasUsage   bool
}

// emitNodeStart notifies the event handler that a node has started.
func (de *dagExecutor) emitNodeStart(nodeID string) {
	if de.cfg.EventHandler != nil {
		de.cfg.EventHandler.OnNodeStart(nodeID)
	}
}

// emitNodeComplete notifies the event handler that a node has completed.
func (de *dagExecutor) emitNodeComplete(nodeID string, status NodeStatus, err error) {
	if de.cfg.EventHandler != nil {
		de.cfg.EventHandler.OnNodeComplete(nodeID, status, err)
	}
}

// emitCheckpointError notifies the event handler of a checkpoint save failure.
func (de *dagExecutor) emitCheckpointError(nodeID string, err error) {
	if de.cfg.EventHandler != nil {
		de.cfg.EventHandler.OnCheckpointError(nodeID, err)
	}
}

// saveCheckpoint saves a node result and reports errors via the event handler.
func (de *dagExecutor) saveCheckpoint(nodeID string, resp *schema.RunResponse) {
	if de.cfg.CheckpointStore == nil {
		return
	}
	if err := de.cfg.CheckpointStore.Save(de.ctx, de.req.SessionID, nodeID, resp); err != nil {
		de.emitCheckpointError(nodeID, err)
	}
}

type nodeCompletion struct {
	nodeID string
	resp   *schema.RunResponse
	err    error
}

func newDagExecutor(ctx context.Context, cfg DAGConfig, nodes []Node, req *schema.RunRequest) (*dagExecutor, error) {
	if err := ValidateDAG(nodes); err != nil {
		return nil, err
	}

	// Apply critical path priorities if enabled.
	if cfg.PriorityScheduling && cfg.CriticalPathAuto {
		cpPriorities := ComputeCriticalPath(nodes)
		for i := range nodes {
			if nodes[i].Priority == 0 {
				nodes[i].Priority = cpPriorities[nodes[i].ID]
			}
		}
	}

	nodeMap := make(map[string]*Node, len(nodes))
	for i := range nodes {
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	downstream, inDegree := buildDAGMaps(nodes)

	terminalNodes := make(map[string]bool)
	for _, n := range nodes {
		if len(downstream[n.ID]) == 0 {
			terminalNodes[n.ID] = true
		}
	}

	result := &DAGResult{
		NodeResults: make(map[string]*schema.RunResponse, len(nodes)),
		NodeStatus:  make(map[string]NodeStatus, len(nodes)),
	}
	for _, n := range nodes {
		result.NodeStatus[n.ID] = NodePending
	}

	// Resume from checkpoint if available.
	if cfg.CheckpointStore != nil {
		checkpointData, err := cfg.CheckpointStore.LoadAll(ctx, req.SessionID)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: failed to load checkpoints: %w", err)
		}
		for nodeID, resp := range checkpointData {
			if _, ok := nodeMap[nodeID]; ok {
				result.NodeResults[nodeID] = resp
				result.NodeStatus[nodeID] = NodeDone
			}
		}
	}

	// Initialize resource manager.
	var resMgr *resourceManager
	if len(cfg.ResourceLimits) > 0 || len(cfg.ResourceRateLimits) > 0 {
		resMgr = newResourceManager(cfg.ResourceLimits, cfg.ResourceRateLimits)
	}

	// Initialize backpressure controller.
	var bpCtrl *backpressureController
	if cfg.BackpressureCfg != nil {
		bpCtrl = newBackpressureController(cfg.BackpressureCfg)
	}

	parentCtx := ctx
	childCtx, cancel := context.WithCancel(ctx)

	var sem chan struct{}
	if cfg.MaxConcurrency > 0 && bpCtrl == nil {
		sem = make(chan struct{}, cfg.MaxConcurrency)
	}

	de := &dagExecutor{
		cfg:           cfg,
		nodes:         nodes,
		nodeMap:       nodeMap,
		req:           req,
		result:        result,
		timeline:      newTimelineTracker(),
		resMgr:        resMgr,
		bpCtrl:        bpCtrl,
		sem:           sem,
		doneCh:        make(chan nodeCompletion, len(nodes)),
		parentCtx:     parentCtx,
		ctx:           childCtx,
		cancel:        cancel,
		downstream:    downstream,
		inDegree:      inDegree,
		terminalNodes: terminalNodes,
	}

	// Count pending nodes (not already completed from checkpoint).
	for _, n := range nodes {
		if result.NodeStatus[n.ID] == NodePending {
			de.pending++
		}
	}

	// Update in-degrees for checkpoint-resumed nodes.
	for _, n := range nodes {
		if result.NodeStatus[n.ID] == NodeDone {
			for _, dsID := range downstream[n.ID] {
				inDegree[dsID]--
			}
		}
	}

	// Use priority queue if enabled.
	if cfg.PriorityScheduling {
		de.pq = newPriorityQueue()
		de.sem = nil
	}

	return de, nil
}

// run executes the DAG event loop.
func (de *dagExecutor) run() (*DAGResult, error) {
	de.mu.Lock()
	for _, n := range de.nodes {
		if de.inDegree[n.ID] == 0 && de.result.NodeStatus[n.ID] == NodePending {
			de.enqueueReady(n.ID)
		}
	}
	de.drainPQ()
	de.mu.Unlock()

	var firstErr error
	for de.pending > 0 {
		select {
		case comp := <-de.doneCh:
			de.pending--
			done, err := de.handleCompletion(comp)
			if err != nil {
				firstErr = err
			}
			if done {
				if firstErr != nil {
					return de.result, firstErr
				}
				return de.finalize()
			}

		case <-de.ctx.Done():
			de.mu.Lock()
			running := countRunning(de.result)
			de.mu.Unlock()
			return de.abort(running, de.ctx.Err())
		}
	}

	return de.finalize()
}

// abort performs the shared teardown for every terminating branch (error,
// cancellation, or early exit): it cancels the shared context, drains the
// given number of still-running node completions from doneCh so their
// goroutines can exit, runs any afterDrain hooks (used by backward
// compensation, which must run after draining but before writeback), then
// writes back Timeline and available Usage and returns the current result
// paired with err. Cancelling an already-cancelled context and draining zero
// completions are both no-ops, so cancellation-path callers may reuse it.
// Caller must NOT hold de.mu.
func (de *dagExecutor) abort(running int, err error, afterDrain ...func()) (*DAGResult, error) {
	de.cancel()
	drainDoneCh(de.doneCh, running)
	for _, fn := range afterDrain {
		fn()
	}
	de.result.Timeline = de.timeline.result()
	if de.hasUsage {
		de.result.Usage = &de.totalUsage
	}
	return de.result, err
}

// handleCompletion processes a single node completion event.
// Returns (done bool, err error). If done is true, the caller should exit the loop.
// Caller must NOT hold de.mu.
func (de *dagExecutor) handleCompletion(comp nodeCompletion) (bool, error) {
	de.mu.Lock()
	if de.pq != nil {
		de.activePQ--
	}

	if comp.err != nil {
		return de.handleNodeError(comp)
	}

	return de.handleNodeSuccess(comp)
}

// handleNodeError handles a failed node. Caller must hold de.mu.
func (de *dagExecutor) handleNodeError(comp nodeCompletion) (bool, error) {
	de.result.NodeStatus[comp.nodeID] = NodeFailed
	de.emitNodeComplete(comp.nodeID, NodeFailed, comp.err)
	node := de.nodeMap[comp.nodeID]

	// Handle Compensate error strategy.
	if de.cfg.ErrorStrategy == Compensate && de.cfg.CompensateCfg != nil {
		if de.cfg.CompensateCfg.Strategy == ForwardRecovery {
			// Try forward recovery.
			nodeReq, buildErr := buildNodeInput(node, de.req, de.result.NodeResults)
			if buildErr == nil {
				de.mu.Unlock()
				resp, fwdErr := executeForwardRecovery(de.ctx, de.cfg.CompensateCfg, node, nodeReq)
				de.mu.Lock()
				if fwdErr == nil {
					de.result.NodeStatus[comp.nodeID] = NodeDone
					de.result.NodeResults[comp.nodeID] = resp
					de.emitNodeComplete(comp.nodeID, NodeDone, nil)
					if resp.Usage != nil {
						de.hasUsage = true
						de.totalUsage.Add(resp.Usage)
					}
					de.saveCheckpoint(comp.nodeID, resp)
					de.timeline.recordEnd(comp.nodeID, NodeDone)
					de.propagateDownstream(comp.nodeID)
					de.drainPQ()
					de.mu.Unlock()
					return false, nil
				}
			}
		}

		// Backward compensation runs after cancel+drain, before the shared
		// Timeline/Usage writeback.
		firstErr := fmt.Errorf("orchestrate: node %q failed: %w", comp.nodeID, comp.err)
		running := countRunning(de.result)
		de.mu.Unlock()
		_, _ = de.abort(running, firstErr, func() {
			_ = executeBackwardCompensation(de.parentCtx, de.cfg.CompensateCfg, de.nodes, de.result)
		})
		return true, firstErr
	}

	if de.cfg.ErrorStrategy == Abort || !node.Optional {
		firstErr := fmt.Errorf("orchestrate: node %q failed: %w", comp.nodeID, comp.err)
		running := countRunning(de.result)
		de.mu.Unlock()
		_, _ = de.abort(running, firstErr)
		return true, firstErr
	}

	de.result.NodeStatus[comp.nodeID] = NodeSkipped
	de.propagateDownstream(comp.nodeID)
	de.drainPQ()
	de.mu.Unlock()
	return false, nil
}

// handleNodeSuccess handles a successful node. Caller must hold de.mu.
func (de *dagExecutor) handleNodeSuccess(comp nodeCompletion) (bool, error) {
	de.result.NodeStatus[comp.nodeID] = NodeDone
	de.result.NodeResults[comp.nodeID] = comp.resp
	de.emitNodeComplete(comp.nodeID, NodeDone, nil)

	if comp.resp != nil && comp.resp.Usage != nil {
		de.hasUsage = true
		de.totalUsage.Add(comp.resp.Usage)
	}

	de.saveCheckpoint(comp.nodeID, comp.resp)

	if de.cfg.EarlyExitFunc != nil && de.cfg.EarlyExitFunc(comp.nodeID, comp.resp) {
		running := countRunning(de.result)
		de.mu.Unlock()
		_, _ = de.abort(running, nil)
		return true, nil
	}

	// Check if context was cancelled before launching downstream.
	if de.ctx.Err() != nil {
		running := countRunning(de.result)
		de.mu.Unlock()
		_, _ = de.abort(running, de.ctx.Err())
		return true, de.ctx.Err()
	}

	de.propagateDownstream(comp.nodeID)
	de.drainPQ()
	de.mu.Unlock()
	return false, nil
}

// propagateDownstream decrements in-degrees and enqueues newly ready nodes.
// Caller must hold de.mu.
func (de *dagExecutor) propagateDownstream(nodeID string) {
	for _, dsID := range de.downstream[nodeID] {
		de.inDegree[dsID]--
		if de.inDegree[dsID] == 0 && de.result.NodeStatus[dsID] == NodePending {
			de.enqueueReady(dsID)
		}
	}
}

// scheduleReady checks a ready node's condition, skips it if false (recursively
// propagating to downstream), or launches it. Caller must hold de.mu.
func (de *dagExecutor) scheduleReady(nodeID string) {
	node := de.nodeMap[nodeID]

	// In replay mode, check checkpoint.
	if de.cfg.ReplayMode && de.cfg.CheckpointStore != nil {
		if de.result.NodeStatus[nodeID] == NodeDone {
			de.pending--
			de.propagateDownstream(nodeID)
			return
		}
	}

	if node.Condition != nil {
		upResults := make(map[string]*schema.RunResponse, len(node.Deps))
		for _, dep := range node.Deps {
			upResults[dep] = de.result.NodeResults[dep]
		}
		if !node.Condition(upResults) {
			de.result.NodeStatus[nodeID] = NodeSkipped
			de.pending--
			de.timeline.recordStart(nodeID)
			de.timeline.recordEnd(nodeID, NodeSkipped)
			de.propagateDownstream(nodeID)
			return
		}
	}

	nodeReq, err := buildNodeInput(node, de.req, de.result.NodeResults)
	if err != nil {
		de.result.NodeStatus[nodeID] = NodeRunning
		de.doneCh <- nodeCompletion{nodeID: nodeID, err: err}
		return
	}
	de.emitNodeStart(nodeID)
	launchNodeAdvanced(de.ctx, node, nodeReq, de.sem, de.bpCtrl, de.resMgr, de.timeline, de.result, de.doneCh)
}

// enqueueReady adds a node to the ready queue. Caller must hold de.mu.
func (de *dagExecutor) enqueueReady(nodeID string) {
	if de.pq != nil {
		de.pq.push(nodeID, de.nodeMap[nodeID].Priority)
	} else {
		de.scheduleReady(nodeID)
	}
}

// drainPQ launches ready nodes from the priority queue up to the concurrency limit.
// Caller must hold de.mu.
func (de *dagExecutor) drainPQ() {
	if de.pq == nil {
		return
	}
	maxActive := max(de.cfg.MaxConcurrency, 0)
	for !de.pq.empty() {
		if maxActive > 0 && de.activePQ >= maxActive {
			break
		}
		nodeID := de.pq.pop()
		de.scheduleReady(nodeID)
		if de.result.NodeStatus[nodeID] == NodeRunning {
			de.activePQ++
		}
	}
}

// finalize aggregates terminal node results into the final output.
func (de *dagExecutor) finalize() (*DAGResult, error) {
	if de.hasUsage {
		de.result.Usage = &de.totalUsage
	}
	de.result.Timeline = de.timeline.result()

	termResults := make(map[string]*schema.RunResponse)
	for id := range de.terminalNodes {
		if de.result.NodeStatus[id] == NodeDone {
			termResults[id] = de.result.NodeResults[id]
		}
	}

	// If no terminal nodes completed (e.g. early exit before they ran),
	// fall back to all completed node results.
	if len(termResults) == 0 {
		for id, status := range de.result.NodeStatus {
			if status == NodeDone {
				termResults[id] = de.result.NodeResults[id]
			}
		}
	}

	agg := de.cfg.Aggregator
	if agg == nil {
		agg = ConcatMessagesAggregator()
	}

	finalOutput, err := agg.Aggregate(de.parentCtx, termResults)
	if err != nil {
		return de.result, fmt.Errorf("orchestrate: aggregation failed: %w", err)
	}
	de.result.FinalOutput = finalOutput

	return de.result, nil
}

// launchNodeAdvanced starts a goroutine to run a node with timeout, retries,
// backpressure, and resource management support. Caller must hold mu.
func launchNodeAdvanced(ctx context.Context, node *Node, req *schema.RunRequest,
	sem chan struct{}, bpCtrl *backpressureController, resMgr *resourceManager,
	timeline *timelineTracker, result *DAGResult, doneCh chan<- nodeCompletion,
) {
	result.NodeStatus[node.ID] = NodeRunning
	timeline.recordStart(node.ID)

	go func() {
		// Acquire global concurrency slot.
		if bpCtrl != nil {
			if err := bpCtrl.acquire(ctx); err != nil {
				timeline.recordEnd(node.ID, NodeFailed)
				doneCh <- nodeCompletion{nodeID: node.ID, err: err}
				return
			}
			startTime := time.Now()
			defer func() {
				bpCtrl.release(time.Since(startTime))
			}()
		} else if sem != nil {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				timeline.recordEnd(node.ID, NodeFailed)
				doneCh <- nodeCompletion{nodeID: node.ID, err: ctx.Err()}
				return
			}
		}

		// Acquire resource slots.
		if resMgr != nil && len(node.ResourceTags) > 0 {
			if err := resMgr.acquire(ctx, node.ResourceTags); err != nil {
				timeline.recordEnd(node.ID, NodeFailed)
				doneCh <- nodeCompletion{nodeID: node.ID, err: err}
				return
			}
			defer resMgr.release(node.ResourceTags)
		}

		// Execute with timeout and retries.
		maxAttempts := max(node.Retries+1, 1)

		var lastErr error
		for attempt := range maxAttempts {
			resp, err := runWithTimeout(ctx, node, req)
			if err == nil {
				if resp == nil {
					lastErr = fmt.Errorf("nil response from node %q", node.ID)
				} else {
					timeline.recordEnd(node.ID, NodeDone)
					doneCh <- nodeCompletion{nodeID: node.ID, resp: resp}
					return
				}
			} else {
				lastErr = err
			}

			// Retry with backoff if not last attempt.
			if attempt < maxAttempts-1 {
				backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
				select {
				case <-ctx.Done():
					timeline.recordEnd(node.ID, NodeFailed)
					doneCh <- nodeCompletion{nodeID: node.ID, err: ctx.Err()}
					return
				case <-time.After(backoff):
				}
			}
		}

		timeline.recordEnd(node.ID, NodeFailed)
		doneCh <- nodeCompletion{nodeID: node.ID, err: lastErr}
	}()
}

// runWithTimeout runs a node's runner with an optional per-node timeout.
func runWithTimeout(ctx context.Context, node *Node, req *schema.RunRequest) (*schema.RunResponse, error) {
	return runRunnerWithTimeout(ctx, node.Timeout, node.Runner, req)
}

func buildNodeInput(node *Node, originalReq *schema.RunRequest, results map[string]*schema.RunResponse) (*schema.RunRequest, error) {
	if node.InputMapper != nil {
		upstreamResults := make(map[string]*schema.RunResponse, len(node.Deps))
		for _, dep := range node.Deps {
			upstreamResults[dep] = results[dep]
		}
		mapped, err := node.InputMapper(upstreamResults)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: InputMapper for node %q: %w", node.ID, err)
		}
		return mapped, nil
	}

	if len(node.Deps) == 0 {
		return originalReq, nil
	}

	if len(node.Deps) == 1 {
		depResp := results[node.Deps[0]]
		if depResp != nil {
			return &schema.RunRequest{
				Messages:  depResp.Messages,
				SessionID: originalReq.SessionID,
				Options:   originalReq.Options,
				Metadata:  originalReq.Metadata,
			}, nil
		}
		return originalReq, nil
	}

	// Multiple deps: concatenate messages in Deps declaration order.
	var msgs []schema.Message
	for _, dep := range node.Deps {
		if r := results[dep]; r != nil {
			msgs = append(msgs, r.Messages...)
		}
	}
	return &schema.RunRequest{
		Messages:  msgs,
		SessionID: originalReq.SessionID,
		Options:   originalReq.Options,
		Metadata:  originalReq.Metadata,
	}, nil
}

func detectCycle(nodes []Node) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int)
	adj := make(map[string][]string)
	for _, n := range nodes {
		color[n.ID] = white
		for _, dep := range n.Deps {
			adj[dep] = append(adj[dep], n.ID)
		}
	}

	var visit func(id string) error
	visit = func(id string) error {
		color[id] = gray
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				return fmt.Errorf("orchestrate: cycle detected involving node %q", next)
			case white:
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}

	for _, n := range nodes {
		if color[n.ID] == white {
			if err := visit(n.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// countRunning returns the number of nodes currently in NodeRunning status.
func countRunning(result *DAGResult) int {
	running := 0
	for _, s := range result.NodeStatus {
		if s == NodeRunning {
			running++
		}
	}
	return running
}

// drainDoneCh reads count completions from the channel to clean up running goroutines.
func drainDoneCh(doneCh <-chan nodeCompletion, count int) {
	for count > 0 {
		<-doneCh
		count--
	}
}
