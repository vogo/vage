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

package workflow

import (
	"context"
	"fmt"

	"github.com/vogo/vage/schema"
)

// Runner is the minimum execution surface an Agent already satisfies.
// workflow does not import agent, so L1 cannot depend upward on L3.
type Runner interface {
	Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
}

// AdaptRunner binds a Runner to typed state through explicit mappers and
// returns a Node.Run function.
//
// The request mapper must choose Messages, SessionID, Options, and Metadata.
// The response mapper must choose which response fields become which Field
// writes. Nothing is copied into or out of Metadata automatically.
//
// A nil response, a mapper error, a Runner error, or an interrupted response
// (StopReason interrupted or Interrupt != nil) becomes a node error. An
// interrupted response never reaches the output mapper: resume stays a
// top-level Agent concern.
func AdaptRunner[S any](
	runner Runner,
	toRequest func(Snapshot[S]) (*schema.RunRequest, error),
	toPatch func(Snapshot[S], *schema.RunResponse) (Patch[S], error),
) func(context.Context, Snapshot[S]) (Patch[S], error) {
	if runner == nil {
		panic("workflow: AdaptRunner requires a runner")
	}
	if toRequest == nil || toPatch == nil {
		panic("workflow: AdaptRunner requires request and response mappers")
	}

	return func(ctx context.Context, snap Snapshot[S]) (Patch[S], error) {
		req, err := toRequest(snap)
		if err != nil {
			return Patch[S]{}, err
		}
		if req == nil {
			return Patch[S]{}, ErrNilRequest
		}

		resp, err := runner.Run(ctx, req)
		if err != nil {
			return Patch[S]{}, err
		}
		if resp == nil {
			return Patch[S]{}, ErrNilResponse
		}
		if resp.IsInterrupted() {
			return Patch[S]{}, fmt.Errorf("workflow: %w", ErrInterruptedRunner)
		}

		return toPatch(snap, resp)
	}
}
