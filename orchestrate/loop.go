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

// ExecuteLoop runs a loop with the given body runner and termination conditions.
func ExecuteLoop(ctx context.Context, loop LoopNode, req *schema.RunRequest) (*schema.RunResponse, error) {
	if loop.Body == nil {
		return nil, fmt.Errorf("orchestrate: loop body is nil")
	}

	// Zero-iteration pre-check: if condition returns false for nil, skip all iterations.
	if loop.Condition != nil && !loop.Condition(nil) {
		return &schema.RunResponse{
			Messages:  req.Messages,
			SessionID: req.SessionID,
		}, nil
	}

	currentReq := req
	var prevResp *schema.RunResponse
	var totalUsage schema.Usage
	hasUsage := false
	var lastResp *schema.RunResponse

	for iter := 0; loop.MaxIters <= 0 || iter < loop.MaxIters; iter++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := loop.Body.Run(ctx, currentReq)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: loop iteration %d: %w", iter, err)
		}

		if resp == nil {
			return nil, fmt.Errorf("orchestrate: loop iteration %d: nil response", iter)
		}

		lastResp = resp

		if resp.Usage != nil {
			hasUsage = true
			totalUsage.Add(resp.Usage)
		}

		if loop.Condition != nil && !loop.Condition(resp) {
			break
		}

		if loop.ConvergenceFunc != nil && prevResp != nil && loop.ConvergenceFunc(prevResp, resp) {
			break
		}

		prevResp = resp
		currentReq = &schema.RunRequest{
			Messages:  resp.Messages,
			SessionID: req.SessionID,
			Options:   req.Options,
			Metadata:  req.Metadata,
		}
	}

	if lastResp == nil {
		return &schema.RunResponse{Messages: req.Messages, SessionID: req.SessionID}, nil
	}

	result := &schema.RunResponse{
		Messages:  lastResp.Messages,
		Metadata:  lastResp.Metadata,
		SessionID: req.SessionID,
	}
	if hasUsage {
		result.Usage = &totalUsage
	}
	return result, nil
}
