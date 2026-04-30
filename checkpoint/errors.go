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

package checkpoint

import "errors"

// Sentinel errors returned by IterationStore implementations and
// TaskAgent.Resume. Match with errors.Is to remain forward-compatible
// with wrapped errors.
var (
	// ErrCheckpointNotFound is returned when Load or Resume cannot
	// locate a matching checkpoint.
	ErrCheckpointNotFound = errors.New("checkpoint: not found")

	// ErrInvalidArgument is returned when input fails validation
	// (nil checkpoint, empty session id, cross-agent mismatch on
	// Resume, missing IterationStore on Resume, etc.). Wrapped with
	// details where helpful.
	ErrInvalidArgument = errors.New("checkpoint: invalid argument")

	// ErrAlreadyFinal is returned by TaskAgent.Resume when the latest
	// checkpoint for the session is Final == true. The caller decides
	// whether to surface the stored response (via Load) or treat the
	// resume request as a no-op.
	ErrAlreadyFinal = errors.New("checkpoint: session already finalized")
)
