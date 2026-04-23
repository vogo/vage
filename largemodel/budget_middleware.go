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

package largemodel

import (
	"context"

	"github.com/vogo/aimodel"
)

// BudgetPreCheckFunc is called before each LLM invocation. Returning a
// non-nil error aborts the call and causes that error to propagate upward.
// The concrete error type is chosen by the caller (e.g. vv/traces/budgets
// returns *BudgetExceededError, which satisfies errors.Is(err, ErrBudgetExceeded)).
type BudgetPreCheckFunc func(ctx context.Context) error

// BudgetPostRecordFunc is called after a successful LLM invocation, with the
// final token usage. For streaming calls it fires on stream close. The caller
// is responsible for aggregating usage into its own trackers and for emitting
// any warn/exceeded events it wants downstream subscribers to see.
type BudgetPostRecordFunc func(ctx context.Context, usage aimodel.Usage)

// BudgetMiddleware gates LLM calls against a host-supplied pre-check and
// records usage via a host-supplied post-record hook. vage/largemodel stays
// free of vv-specific Tracker types: the caller injects two closures.
//
// Ordering: this middleware MUST be outermost so its rejection happens before
// retry/circuit-breaker/cache layers get a chance to duplicate usage.
type BudgetMiddleware struct {
	preCheck   BudgetPreCheckFunc
	postRecord BudgetPostRecordFunc
}

// NewBudgetMiddleware constructs a BudgetMiddleware. Either closure may be
// nil: a nil preCheck disables the gate (the middleware becomes a plain
// post-record observer), and a nil postRecord disables accounting.
// When both are nil the middleware is a no-op; callers should skip inserting
// it in that case.
func NewBudgetMiddleware(preCheck BudgetPreCheckFunc, postRecord BudgetPostRecordFunc) *BudgetMiddleware {
	return &BudgetMiddleware{preCheck: preCheck, postRecord: postRecord}
}

// Wrap implements Middleware.
func (m *BudgetMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			if m.preCheck != nil {
				if err := m.preCheck(ctx); err != nil {
					return nil, err
				}
			}

			resp, err := next.ChatCompletion(ctx, req)
			if err != nil {
				return nil, err
			}

			if m.postRecord != nil {
				m.postRecord(ctx, resp.Usage)
			}

			return resp, nil
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			if m.preCheck != nil {
				if err := m.preCheck(ctx); err != nil {
					return nil, err
				}
			}

			s, err := next.ChatCompletionStream(ctx, req)
			if err != nil {
				return nil, err
			}

			if m.postRecord == nil {
				return s, nil
			}

			return aimodel.WrapStream(s, func(usage *aimodel.Usage) {
				if usage == nil {
					return
				}
				m.postRecord(ctx, *usage)
			}), nil
		},
	}
}
