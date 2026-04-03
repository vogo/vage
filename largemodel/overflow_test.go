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
	"testing"

	"github.com/vogo/aimodel"
)

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "generic error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
		{
			name: "API error status 413",
			err: &aimodel.APIError{
				StatusCode: 413,
				Message:    "payload too large",
			},
			expected: true,
		},
		{
			name: "API error code context_length_exceeded",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "context_length_exceeded",
				Message:    "This model's maximum context length is 128000 tokens.",
			},
			expected: true,
		},
		{
			name: "API error code request_too_large",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "request_too_large",
				Message:    "Request too large.",
			},
			expected: true,
		},
		{
			name: "API error message maximum context length",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "invalid_request_error",
				Message:    "This model's Maximum context length is 200000 tokens.",
			},
			expected: true,
		},
		{
			name: "API error message token limit",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "invalid_request",
				Message:    "Token limit exceeded for this request.",
			},
			expected: true,
		},
		{
			name: "API error message request too large",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "invalid_request",
				Message:    "Request too large for the model.",
			},
			expected: true,
		},
		{
			name: "API error unrelated 400",
			err: &aimodel.APIError{
				StatusCode: 400,
				Code:       "invalid_request",
				Message:    "Invalid JSON in request body",
			},
			expected: false,
		},
		{
			name:     "non-API error with context_length_exceeded",
			err:      errors.New("context_length_exceeded: too many tokens"),
			expected: true,
		},
		{
			name:     "non-API error with maximum context length",
			err:      errors.New("maximum context length exceeded"),
			expected: true,
		},
		{
			name: "wrapped API error",
			err: fmt.Errorf("stream error: %w", &aimodel.APIError{
				StatusCode: 413,
				Message:    "too large",
			}),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsContextOverflowError(tt.err)
			if got != tt.expected {
				t.Errorf("IsContextOverflowError() = %v, want %v", got, tt.expected)
			}
		})
	}
}
