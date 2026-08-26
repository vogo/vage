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
	"container/heap"
	"slices"
)

// priorityItem wraps a node ID with its priority and insertion order for stable sorting.
type priorityItem struct {
	nodeID   string
	priority int
	index    int // position in heap
	order    int // insertion order for tie-breaking
}

// priorityQueue implements heap.Interface for scheduling nodes by priority.
// Higher priority values are dequeued first; ties broken by insertion order (FIFO).
type priorityQueue struct {
	items []*priorityItem
	seq   int
}

func newPriorityQueue() *priorityQueue {
	return &priorityQueue{}
}

func (pq *priorityQueue) Len() int { return len(pq.items) }

func (pq *priorityQueue) Less(i, j int) bool {
	if pq.items[i].priority != pq.items[j].priority {
		return pq.items[i].priority > pq.items[j].priority // higher priority first
	}
	return pq.items[i].order < pq.items[j].order // FIFO for equal priority
}

func (pq *priorityQueue) Swap(i, j int) {
	pq.items[i], pq.items[j] = pq.items[j], pq.items[i]
	pq.items[i].index = i
	pq.items[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	item := x.(*priorityItem)
	item.index = len(pq.items)
	item.order = pq.seq
	pq.seq++
	pq.items = append(pq.items, item)
}

func (pq *priorityQueue) Pop() any {
	old := pq.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	pq.items = old[:n-1]
	return item
}

// push adds a node to the priority queue.
func (pq *priorityQueue) push(nodeID string, priority int) {
	item := &priorityItem{nodeID: nodeID, priority: priority}
	heap.Push(pq, item)
}

// pop removes and returns the highest-priority node ID.
func (pq *priorityQueue) pop() string {
	item := heap.Pop(pq).(*priorityItem)
	return item.nodeID
}

// empty returns true if the queue has no items.
func (pq *priorityQueue) empty() bool {
	return len(pq.items) == 0
}

// ComputeCriticalPath analyzes the DAG topology using the Critical Path Method (CPM)
// and assigns priority values to nodes. Nodes on the critical path get higher priority.
// The weight of each node is assumed to be 1 (unit weight) unless Duration metadata is available.
func ComputeCriticalPath(nodes []Node) map[string]int {
	nodeMap := make(map[string]*Node, len(nodes))
	for i := range nodes {
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	downstream, _ := buildDAGMaps(nodes)
	topoOrder := topologicalSort(nodes)

	// Forward pass: compute earliest start time (EST) for each node.
	// EST(n) = max(EST(dep) + weight(dep)) for all deps.
	est := make(map[string]int, len(nodes))
	weight := make(map[string]int, len(nodes))
	for _, n := range nodes {
		weight[n.ID] = 1 // unit weight
	}

	for _, id := range topoOrder {
		node := nodeMap[id]
		maxEST := 0
		for _, dep := range node.Deps {
			if v := est[dep] + weight[dep]; v > maxEST {
				maxEST = v
			}
		}
		est[id] = maxEST
	}

	// Find project end time.
	projectEnd := 0
	for _, id := range topoOrder {
		if v := est[id] + weight[id]; v > projectEnd {
			projectEnd = v
		}
	}

	// Backward pass: compute latest start time (LST).
	// LST(n) = min(LST(downstream) - weight(n)) for all downstream.
	lst := make(map[string]int, len(nodes))
	for _, n := range nodes {
		lst[n.ID] = projectEnd - weight[n.ID] // initialize to max possible
	}
	// Process in reverse topological order.
	for _, id := range slices.Backward(topoOrder) {
		if len(downstream[id]) == 0 {
			lst[id] = projectEnd - weight[id]
		} else {
			minLST := projectEnd
			for _, ds := range downstream[id] {
				if lst[ds] < minLST {
					minLST = lst[ds]
				}
			}
			lst[id] = minLST - weight[id]
		}
	}

	// Compute slack = LST - EST. Nodes with slack 0 are on the critical path.
	// Priority = projectEnd - slack (so critical path nodes get highest priority).
	priorities := make(map[string]int, len(nodes))
	for _, n := range nodes {
		slack := lst[n.ID] - est[n.ID]
		priorities[n.ID] = projectEnd - slack
	}

	return priorities
}
