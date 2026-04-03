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
	"strings"

	"github.com/vogo/aimodel"
)

// IsContextOverflowError returns true if the error indicates the request
// exceeded the model's context window limit.
// Checks for HTTP 413 (Payload Too Large), error codes, and common error
// message patterns from OpenAI and Anthropic APIs.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *aimodel.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 413 {
			return true
		}

		// Check error codes used by providers.
		switch apiErr.Code {
		case "context_length_exceeded", "request_too_large":
			return true
		}

		// Check common error message patterns.
		msg := strings.ToLower(apiErr.Message)
		if strings.Contains(msg, "context_length_exceeded") ||
			strings.Contains(msg, "maximum context length") ||
			strings.Contains(msg, "token limit") ||
			strings.Contains(msg, "request too large") {
			return true
		}
	}

	// Also check the raw error message for non-API errors.
	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "maximum context length")
}
