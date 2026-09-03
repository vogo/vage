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

import "fmt"

// validateNodes rejects empty IDs, nil run funcs, duplicate IDs, unknown
// dependencies, cycles, and disconnected subgraphs. An empty slice is valid
// (Run returns the initial state).
func validateNodes[S any](nodes []Node[S]) error {
	if len(nodes) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.ID == "" {
			return fmt.Errorf("workflow: empty node ID")
		}
		if n.Run == nil {
			return fmt.Errorf("workflow: node %q has a nil run function", n.ID)
		}
		if seen[n.ID] {
			return fmt.Errorf("workflow: duplicate node ID %q", n.ID)
		}
		seen[n.ID] = true
	}

	for _, n := range nodes {
		for _, dep := range n.Deps {
			if !seen[dep] {
				return fmt.Errorf("workflow: node %q depends on unknown node %q", n.ID, dep)
			}
		}
	}

	if err := detectCycle(nodes); err != nil {
		return err
	}
	return checkConnected(nodes)
}

func detectCycle[S any](nodes []Node[S]) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
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
				return fmt.Errorf("workflow: cycle detected involving node %q", next)
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

func checkConnected[S any](nodes []Node[S]) error {
	if len(nodes) <= 1 {
		return nil
	}

	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		for _, dep := range n.Deps {
			adj[n.ID] = append(adj[n.ID], dep)
			adj[dep] = append(adj[dep], n.ID)
		}
	}

	visited := make(map[string]bool, len(nodes))
	queue := []string{nodes[0].ID}
	visited[nodes[0].ID] = true

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, neighbor := range adj[cur] {
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}

	if len(visited) != len(nodes) {
		for _, n := range nodes {
			if !visited[n.ID] {
				return fmt.Errorf("workflow: node %q is disconnected from the rest of the graph", n.ID)
			}
		}
	}
	return nil
}
