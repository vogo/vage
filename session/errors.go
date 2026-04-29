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

package session

import "errors"

// Sentinel errors returned by SessionStore implementations. Callers should
// match with errors.Is to remain forward-compatible with wrapped errors.
var (
	// ErrSessionNotFound is returned when an operation references a session
	// id that does not exist in the store.
	ErrSessionNotFound = errors.New("session: not found")

	// ErrSessionExists is returned by Create when the id already exists.
	ErrSessionExists = errors.New("session: already exists")

	// ErrInvalidArgument is returned when input fails validation (empty id,
	// id outside IDPattern, nil Session, etc.). Wrapped with details.
	ErrInvalidArgument = errors.New("session: invalid argument")
)
