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

package memory

import (
	"fmt"

	"github.com/vogo/vage/schema"
)

// Budget is the value object for one context build. ModelContextTokens is the
// shared input+output window; 0 means unlimited. Negative fields are illegal.
//
// AvailableHistory is computed by the Builder after charging fixed costs
// (system, request, tools, output). When the window is bounded, 0 means
// history has no remaining quota — it is not the unlimited sentinel.
type Budget struct {
	ModelContextTokens int
	ReservedOutput     int
	ReservedTools      int
	ReservedSystem     int
	AvailableHistory   int
	Estimator          TokenEstimator
}

// CompressionInput is the budget-aware compressor argument.
type CompressionInput struct {
	Messages []schema.Message
	Budget   Budget
}

// Validate reports an error when any budget field is negative.
func (b Budget) Validate() error {
	if b.ModelContextTokens < 0 {
		return fmt.Errorf("memory: ModelContextTokens must not be negative")
	}
	if b.ReservedOutput < 0 {
		return fmt.Errorf("memory: ReservedOutput must not be negative")
	}
	if b.ReservedTools < 0 {
		return fmt.Errorf("memory: ReservedTools must not be negative")
	}
	if b.ReservedSystem < 0 {
		return fmt.Errorf("memory: ReservedSystem must not be negative")
	}
	if b.AvailableHistory < 0 {
		return fmt.Errorf("memory: AvailableHistory must not be negative")
	}
	return nil
}

// Unlimited reports whether the window constraint is disabled.
func (b Budget) Unlimited() bool {
	return b.ModelContextTokens == 0
}

// BoundedZeroHistory reports a bounded window with no remaining history quota.
func (b Budget) BoundedZeroHistory() bool {
	return b.ModelContextTokens > 0 && b.AvailableHistory <= 0
}

// EstimatorOrDefault returns the budget estimator, falling back to
// DefaultTokenEstimator when Estimator is nil.
func (b Budget) EstimatorOrDefault() TokenEstimator {
	if b.Estimator != nil {
		return b.Estimator
	}
	return DefaultTokenEstimator
}
