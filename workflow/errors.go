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
	"errors"
	"fmt"
	"strings"
)

// ErrWriteConflict is the sentinel wrapped by WriteConflictError so callers
// can use errors.Is without depending on the diagnostic text.
var ErrWriteConflict = errors.New("workflow: write conflict")

// ErrInterruptedRunner marks a Runner that suspended for a human decision
// instead of producing a result. AdaptRunner treats this as a node error and
// does not call the output mapper, matching orchestration OR-10: nested
// human-in-the-loop has no resume path here.
var ErrInterruptedRunner = errors.New(
	"runner suspended for a human decision; " +
		"nested human-in-the-loop is not supported — run that agent directly at the top level to resume it",
)

// ErrNilResponse is returned when a Runner succeeds with a nil *schema.RunResponse.
var ErrNilResponse = errors.New("workflow: runner returned a nil response")

// ErrNilRequest is returned when the request mapper succeeds with a nil *schema.RunRequest.
var ErrNilRequest = errors.New("workflow: request mapper returned a nil request")

// WriteConflictError reports that one Patch wrote a Field twice, or that two
// or more nodes in the same logical batch wrote the same Field. Field is the
// diagnostic name; Nodes is the stable-sorted set of node IDs involved.
// No setter from this batch has run when the error is returned.
type WriteConflictError struct {
	Field string
	Nodes []string
}

func (e *WriteConflictError) Error() string {
	if e == nil {
		return "workflow: write conflict"
	}
	return fmt.Sprintf("workflow: write conflict on field %q from nodes %s", e.Field, strings.Join(e.Nodes, ", "))
}

func (e *WriteConflictError) Unwrap() error { return ErrWriteConflict }

// NodeError annotates a node execution, mapper, or runner failure with the
// node ID so callers can unwrap the original error.
type NodeError struct {
	NodeID string
	Err    error
}

func (e *NodeError) Error() string {
	if e == nil {
		return "workflow: node error"
	}
	return fmt.Sprintf("workflow: node %q: %v", e.NodeID, e.Err)
}

func (e *NodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
