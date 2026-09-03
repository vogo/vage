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
	"errors"
	"fmt"
	"sort"
	"sync"
)

type nodeResult[S any] struct {
	id    string
	patch Patch[S]
	err   error
}

func (w *Workflow[S]) execute(ctx context.Context, initial S) (S, error) {
	state := initial
	pending := make(map[string]Node[S], len(w.nodes))
	remainingDeps := make(map[string]int, len(w.nodes))
	downstream := make(map[string][]string, len(w.nodes))

	for _, n := range w.nodes {
		pending[n.ID] = n
		remainingDeps[n.ID] = len(n.Deps)
		for _, dep := range n.Deps {
			downstream[dep] = append(downstream[dep], n.ID)
		}
	}

	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return state, fmt.Errorf("workflow: %w", err)
		}

		batch := readyBatch(pending, remainingDeps)
		if len(batch) == 0 {
			return state, errors.New("workflow: internal: no ready nodes in a validated graph")
		}

		snap := Snapshot[S]{state: state}
		results, err := w.runBatch(ctx, snap, batch)
		if err != nil {
			return state, err
		}
		if err := mergeBatch(results, &state); err != nil {
			return state, err
		}

		for _, n := range batch {
			delete(pending, n.ID)
			for _, ds := range downstream[n.ID] {
				remainingDeps[ds]--
			}
		}
	}

	return state, nil
}

func readyBatch[S any](pending map[string]Node[S], remainingDeps map[string]int) []Node[S] {
	var batch []Node[S]
	for id, n := range pending {
		if remainingDeps[id] == 0 {
			batch = append(batch, n)
		}
	}
	sort.Slice(batch, func(i, j int) bool { return batch[i].ID < batch[j].ID })
	return batch
}

func (w *Workflow[S]) runBatch(ctx context.Context, snap Snapshot[S], batch []Node[S]) ([]nodeResult[S], error) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var sem chan struct{}
	if w.maxConcurrency > 0 {
		sem = make(chan struct{}, w.maxConcurrency)
	}

	results := make([]nodeResult[S], len(batch))
	var wg sync.WaitGroup
	var failOnce sync.Once

	for i, n := range batch {
		wg.Add(1)
		go func(i int, n Node[S]) {
			defer wg.Done()

			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-batchCtx.Done():
					results[i] = nodeResult[S]{id: n.ID, err: batchCtx.Err()}
					return
				}
			}

			patch, err := n.Run(batchCtx, snap)
			if err != nil {
				failOnce.Do(cancel)
			}
			results[i] = nodeResult[S]{id: n.ID, patch: patch, err: err}
		}(i, n)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}

	var (
		nodeErr error
		nodeID  string
	)
	for _, r := range results {
		if r.err == nil {
			continue
		}
		if isCancelErr(r.err) {
			continue
		}
		if nodeErr == nil || r.id < nodeID {
			nodeErr = r.err
			nodeID = r.id
		}
	}
	if nodeErr != nil {
		return nil, &NodeError{NodeID: nodeID, Err: nodeErr}
	}
	for _, r := range results {
		if r.err != nil {
			return nil, &NodeError{NodeID: r.id, Err: r.err}
		}
	}
	return results, nil
}

func isCancelErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func mergeBatch[S any](results []nodeResult[S], state *S) error {
	type fieldWriters struct {
		name  string
		nodes []string
	}
	writers := make(map[*fieldIdentity]*fieldWriters)

	for _, r := range results {
		seen := make(map[*fieldIdentity]struct{}, len(r.patch.changes))
		for _, ch := range r.patch.changes {
			if ch.id == nil {
				return fmt.Errorf("workflow: node %q returned a patch with a zero Change", r.id)
			}
			if _, dup := seen[ch.id]; dup {
				return &WriteConflictError{Field: ch.name, Nodes: []string{r.id}}
			}
			seen[ch.id] = struct{}{}
			ow, ok := writers[ch.id]
			if !ok {
				ow = &fieldWriters{name: ch.name}
				writers[ch.id] = ow
			}
			ow.nodes = append(ow.nodes, r.id)
		}
	}

	var conflict *WriteConflictError
	for _, ow := range writers {
		if len(ow.nodes) < 2 {
			continue
		}
		nodes := append([]string(nil), ow.nodes...)
		sort.Strings(nodes)
		c := &WriteConflictError{Field: ow.name, Nodes: nodes}
		if conflict == nil || c.Field < conflict.Field || (c.Field == conflict.Field && nodesLess(c.Nodes, conflict.Nodes)) {
			conflict = c
		}
	}
	if conflict != nil {
		return conflict
	}

	ordered := append([]nodeResult[S](nil), results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].id < ordered[j].id })
	for _, r := range ordered {
		for _, ch := range r.patch.changes {
			ch.apply(state)
		}
	}
	return nil
}

func nodesLess(a, b []string) bool {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
