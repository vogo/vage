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
)

// Node is one vertex in a typed workflow. ID must be stable and non-empty.
// Deps lists predecessor IDs. Run sees one committed Snapshot and returns a
// Patch; it must not mutate snapshot state through shared references.
type Node[S any] struct {
	ID   string
	Deps []string
	Run  func(context.Context, Snapshot[S]) (Patch[S], error)
}

// Option configures execution-layer behaviour. Options must not change
// logical batches, snapshot versions, or conflict outcomes.
type Option func(*options)

type options struct {
	maxConcurrency int
}

// WithMaxConcurrency caps how many node goroutines may run at once.
// Zero (the default) means no extra cap. Negative values are rejected at New.
func WithMaxConcurrency(n int) Option {
	return func(o *options) {
		o.maxConcurrency = n
	}
}

// Workflow is a reusable typed graph. Concurrent Run calls are independent:
// each starts from the initial S it is given.
type Workflow[S any] struct {
	nodes          []Node[S]
	maxConcurrency int
}

// New constructs a reusable workflow. The graph is validated once; an empty
// node list is allowed and Run returns the initial state unchanged.
func New[S any](nodes []Node[S], opts ...Option) (*Workflow[S], error) {
	var cfg options
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.maxConcurrency < 0 {
		return nil, fmt.Errorf("workflow: max concurrency must be >= 0")
	}

	cloned := cloneNodes(nodes)
	if err := validateNodes(cloned); err != nil {
		return nil, err
	}

	return &Workflow[S]{
		nodes:          cloned,
		maxConcurrency: cfg.maxConcurrency,
	}, nil
}

// Run executes the graph from initial. On success it returns the fully
// merged state. On failure it returns the last fully committed version
// (the initial value if the first batch did not commit) and an error.
func (w *Workflow[S]) Run(ctx context.Context, initial S) (S, error) {
	if w == nil {
		var zero S
		return zero, fmt.Errorf("workflow: nil Workflow")
	}
	if err := ctx.Err(); err != nil {
		return initial, fmt.Errorf("workflow: %w", err)
	}
	if len(w.nodes) == 0 {
		return initial, nil
	}
	return w.execute(ctx, initial)
}

func cloneNodes[S any](nodes []Node[S]) []Node[S] {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]Node[S], len(nodes))
	for i, n := range nodes {
		n.Deps = append([]string(nil), n.Deps...)
		out[i] = n
	}
	return out
}
