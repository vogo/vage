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

package eval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vogo/vage/schema"
)

var _ Evaluator = (*CompositeEvaluator)(nil)

// WeightedEvaluator pairs an evaluator with a weight for composite scoring.
type WeightedEvaluator struct {
	// Evaluator is the evaluator to run.
	Evaluator Evaluator
	// Weight is the weight for this evaluator's score in the final average.
	// If all weights are zero, equal weighting is used.
	Weight float64
}

// CompositeConfig configures the CompositeEvaluator.
type CompositeConfig struct {
	// FailFast controls whether the evaluator stops on the first sub-evaluator error.
	// If false (default), all sub-evaluators are run and errors are collected.
	FailFast bool
}

// CompositeEvaluator runs multiple evaluators on a single case and aggregates results.
type CompositeEvaluator struct {
	evaluators []WeightedEvaluator
	failFast   bool
}

// NewCompositeEvaluator creates a CompositeEvaluator from the given weighted evaluators.
// If cfg is nil, default configuration (non-fail-fast) is used.
func NewCompositeEvaluator(cfg *CompositeConfig, evaluators ...WeightedEvaluator) (*CompositeEvaluator, error) {
	failFast := false
	if cfg != nil {
		failFast = cfg.FailFast
	}

	return &CompositeEvaluator{evaluators: evaluators, failFast: failFast}, nil
}

// Evaluate implements Evaluator. Runs all sub-evaluators sequentially.
// If FailFast is true, any sub-evaluator error is propagated immediately.
// If FailFast is false, errors are collected and reported in Details.
func (e *CompositeEvaluator) Evaluate(ctx context.Context, c *EvalCase) (*EvalResult, error) {
	start := time.Now()

	if len(e.evaluators) == 0 {
		return &EvalResult{
			CaseID:   c.ID,
			Score:    1.0,
			Passed:   true,
			Duration: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine if all weights are zero.
	allZero := true

	for _, we := range e.evaluators {
		if we.Weight != 0 {
			allZero = false

			break
		}
	}

	// Compute effective weights.
	n := float64(len(e.evaluators))
	weights := make([]float64, len(e.evaluators))

	for i, we := range e.evaluators {
		if allZero {
			weights[i] = 1.0 / n
		} else {
			weights[i] = we.Weight
		}
	}

	// Compute sum of weights for normalization.
	weightSum := 0.0
	for _, w := range weights {
		weightSum += w
	}

	var (
		allDetails    []EvalDetail
		weightedScore float64
		passed        = true
		usage         *schema.Usage
		errMessages   []string
	)

	if c.Actual != nil {
		usage = c.Actual.Usage
	}

	for i, we := range e.evaluators {
		result, err := we.Evaluator.Evaluate(ctx, c)
		if err != nil {
			if e.failFast {
				return nil, err
			}

			errMessages = append(errMessages, err.Error())

			allDetails = append(allDetails, EvalDetail{
				Name:    fmt.Sprintf("evaluator_%d", i),
				Score:   0,
				Passed:  false,
				Message: fmt.Sprintf("error: %v", err),
			})

			passed = false

			continue
		}

		allDetails = append(allDetails, result.Details...)

		if weightSum > 0 {
			weightedScore += result.Score * weights[i] / weightSum
		}

		if !result.Passed {
			passed = false
		}

		if result.Usage != nil && usage == nil {
			usage = result.Usage
		}
	}

	evalResult := &EvalResult{
		CaseID:   c.ID,
		Score:    weightedScore,
		Passed:   passed,
		Details:  allDetails,
		Duration: time.Since(start).Milliseconds(),
		Usage:    usage,
	}

	if len(errMessages) > 0 {
		evalResult.Error = strings.Join(errMessages, "; ")
	}

	return evalResult, nil
}
