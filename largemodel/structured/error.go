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

package structured

import (
	"fmt"

	"github.com/vogo/vage/largemodel"
)

// Stage names the step at which a structured call failed.
type Stage string

const (
	StageConfig     Stage = "config"
	StageCapability Stage = "capability"
	StageTransport  Stage = "transport"
	StageDecode     Stage = "decode"
	StageSchema     Stage = "schema"
	StageValidate   Stage = "validate"
)

// Error is a failed structured call. Response and RawText retain the last
// non-empty model output, including any code fences the model emitted.
type Error struct {
	Stage    Stage
	Err      error
	Response *largemodel.Response
	RawText  string
}

func (e *Error) Error() string {
	if e == nil {
		return "vage: structured call failed"
	}

	if e.Err != nil {
		return fmt.Sprintf("vage: structured call failed at %s: %v", e.Stage, e.Err)
	}

	return fmt.Sprintf("vage: structured call failed at %s", e.Stage)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}
