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

package vctx

import (
	"github.com/vogo/vage/session/tree/vectorhook"
	"github.com/vogo/vage/vector"
)

// NonPathNodesPredicate returns a vector.Predicate that drops any
// Document whose Metadata[node_id] is on the given path. This
// implements the §4.8.3 algorithm requirement
// `vector_recall(intent, scope=non_path_nodes, top_k=5)`: when the
// session tree already injects every node on the root → cursor path
// via WorkspaceSource / SessionTreeSource, recalling those same nodes
// from the vector store is wasted budget — the path nodes' summaries
// are already in the prompt verbatim.
//
// path is treated as an unordered set internally. Empty / nil path
// returns nil (the predicate is just dropped — VectorRecallSource
// handles a nil Predicate as "no filter").
//
// Documents that lack a `node_id` metadata key (e.g. archive-hook
// records of the agent's final message, which only carry session_id)
// are KEPT — the predicate's contract is "exclude path tree nodes",
// not "exclude everything not produced by vectorhook".
func NonPathNodesPredicate(path []string) func(d vector.Document) bool {
	if len(path) == 0 {
		return nil
	}
	exclude := make(map[string]struct{}, len(path))
	for _, id := range path {
		if id != "" {
			exclude[id] = struct{}{}
		}
	}
	if len(exclude) == 0 {
		return nil
	}
	return func(d vector.Document) bool {
		nid, ok := d.Metadata[vectorhook.MetadataKeyNodeID].(string)
		if !ok || nid == "" {
			// No node_id ⇒ not a tree node ⇒ keep.
			return true
		}
		_, drop := exclude[nid]
		return !drop
	}
}
