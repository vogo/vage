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

package largemodel

import (
	"errors"
	"fmt"

	"github.com/vogo/vage/schema"
)

// ErrCapabilityUnavailable is the stable sentinel errors.Is matches for a
// capability failure. The typed detail is on *CapabilityError.
var ErrCapabilityUnavailable = errors.New("vage: model capability unavailable")

// CapabilityError reports that a call's requirements were not met by a single
// routable candidate, or that a capability query failed. It is returned before
// any backend I/O. It never carries API keys, private headers, or extension
// values.
type CapabilityError struct {
	Protocol    schema.Protocol
	Model       string
	Required    Requirements
	Known       Capabilities
	Unsatisfied []string
	Err         error
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return ErrCapabilityUnavailable.Error()
	}

	if e.Err != nil {
		return fmt.Sprintf("vage: model capability query failed (protocol %s model %q): %v", e.Protocol, e.Model, e.Err)
	}

	return fmt.Sprintf("vage: model capability unavailable (protocol %s model %q): missing %v", e.Protocol, e.Model, e.Unsatisfied)
}

func (e *CapabilityError) Unwrap() error { return e.Err }

func (e *CapabilityError) Is(target error) bool {
	return target == ErrCapabilityUnavailable
}

// UnsupportedParameterError reports a request field the target protocol cannot
// express. It is returned before any backend I/O; the value is not included
// so logs cannot leak extension payloads through this error.
type UnsupportedParameterError struct {
	Protocol  schema.Protocol
	Parameter string
	Err       error
}

func (e *UnsupportedParameterError) Error() string {
	if e == nil {
		return "vage: unsupported model parameter"
	}

	if e.Err != nil {
		return fmt.Sprintf("vage: unsupported model parameter %q for protocol %s: %v", e.Parameter, e.Protocol, e.Err)
	}

	return fmt.Sprintf("vage: unsupported model parameter %q for protocol %s", e.Parameter, e.Protocol)
}

func (e *UnsupportedParameterError) Unwrap() error { return e.Err }
