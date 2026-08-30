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
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
)

// ErrInterruptedRunner marks a Runner that suspended for a human decision
// instead of producing a result. The engine holds no interrupt id and has no
// resume entry point, so it treats this as a node execution error rather than
// an output.
//
// It is deliberately identifiable with errors.Is: re-running the Runner is not
// ResumeInterrupt — it would start fresh work and can persist a second pending
// record — so node retries and forward recovery stop on it instead of calling
// the Runner again.
var ErrInterruptedRunner = errors.New(
	"runner suspended for a human decision; " +
		"nested human-in-the-loop is not supported — run that agent directly at the top level to resume it",
)

// rejectInterrupted converts a suspended response into an execution error so
// it can never reach node results, checkpoints, input mappers, conditions,
// spawners, aggregators or early-exit checks. where names the consuming
// boundary (a node id, a loop iteration, …). A response that did not suspend
// yields nil and is consumed as before.
func rejectInterrupted(where string, resp *schema.RunResponse) error {
	if !resp.IsInterrupted() {
		return nil
	}

	return fmt.Errorf("orchestrate: %s: %w", where, ErrInterruptedRunner)
}
