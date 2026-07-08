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

package taskagent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// executeToolCall runs a single tool call and returns the result.
func (a *Agent) executeToolCall(ctx context.Context, tc aimodel.ToolCall) schema.ToolResult {
	if a.toolRegistry == nil {
		return schema.ErrorResult(tc.ID, fmt.Sprintf("tool %q: no registry configured", tc.Function.Name))
	}

	tr, err := a.toolRegistry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		return schema.ErrorResult(tc.ID, err.Error())
	}

	tr.ToolCallID = tc.ID
	return tr
}

// executeToolBatch dispatches the tool calls from a single assistant message
// with bounded concurrency, emitting events and returning the tool-result
// messages in ToolCalls[i] order regardless of goroutine finish order.
//
// For 0 or 1 tool calls (or when a.maxParallelToolCalls <= 1) the path
// degenerates to the pre-P1-7 serial loop — no goroutines, no semaphore.
//
// eventSink is called serially from the calling goroutine for every event
// (Start / End / GuardCheck / Result) in the original index order. Returning
// a non-nil error halts dispatch and propagates the error up; the sync-path
// adapter always returns nil, the stream path's send() may fail.
//
// emitResultEvent controls whether EventToolResult is sent after each guard
// pass — true for the streaming path, false for the sync path (which
// appends the result message directly without a user-facing event).
func (a *Agent) executeToolBatch(
	ctx context.Context,
	rc *runContext,
	agentID string,
	toolCalls []aimodel.ToolCall,
	emitResultEvent bool,
	eventSink func(schema.Event) error,
) ([]aimodel.Message, error) {
	n := len(toolCalls)
	if n == 0 {
		return nil, nil
	}

	// Expose sessionID and a stream emitter to tool handlers that want to
	// surface their own events (e.g. todo_write). Injection happens at this
	// single choke point — both sync and stream paths funnel through here.
	ctx = schema.WithSessionID(ctx, rc.sessionID)
	if eventSink != nil {
		ctx = schema.WithEmitter(ctx, schema.Emitter(eventSink))
	}

	// Dispatch all Start events up-front in ToolCalls order so downstream
	// consumers (tests, UI) see a stable sequence regardless of how the
	// workers below complete.
	starts := make([]time.Time, n)
	for i, tc := range toolCalls {
		if err := eventSink(schema.NewEvent(schema.EventToolCallStart, agentID, rc.sessionID, schema.ToolCallStartData{
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Arguments:  tc.Function.Arguments,
		})); err != nil {
			return nil, err
		}
		starts[i] = time.Now()
	}

	results := make([]schema.ToolResult, n)
	durations := make([]time.Duration, n)

	parallelCap := a.maxParallelToolCalls
	if parallelCap <= 0 {
		parallelCap = defaultMaxParallelToolCalls
	}

	if n == 1 || parallelCap <= 1 {
		for i, tc := range toolCalls {
			results[i] = a.executeToolCall(ctx, tc)
			durations[i] = time.Since(starts[i])
		}
	} else {
		if parallelCap > n {
			parallelCap = n
		}
		sem := make(chan struct{}, parallelCap)
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, tc aimodel.ToolCall) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = a.executeToolCall(ctx, tc)
				durations[i] = time.Since(starts[i])
			}(i, tc)
		}
		wg.Wait()
	}

	// Dispatch End / Guard / (optional) Result events + build tool messages
	// in ToolCalls order — matches pre-P1-7 observable sequence.
	toolMsgs := make([]aimodel.Message, 0, n)
	for i, tc := range toolCalls {
		if err := eventSink(schema.NewEvent(schema.EventToolCallEnd, agentID, rc.sessionID, schema.ToolCallEndData{
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Duration:   durations[i].Milliseconds(),
		})); err != nil {
			return nil, err
		}

		res, guardEvt := a.runToolResultGuards(ctx, rc, tc, results[i])
		if guardEvt != nil {
			if err := eventSink(*guardEvt); err != nil {
				return nil, err
			}
		}

		if emitResultEvent {
			if err := eventSink(schema.NewEvent(schema.EventToolResult, agentID, rc.sessionID, schema.ToolResultData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     res,
			})); err != nil {
				return nil, err
			}
		}

		toolMsgs = append(toolMsgs, aimodel.Message{
			Role:       aimodel.RoleTool,
			ToolCallID: res.ToolCallID,
			Content:    aimodel.NewTextContent(toolResultText(res)),
		})
	}

	return toolMsgs, nil
}

// toolResultText extracts the text content from a ToolResult.
func toolResultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}
