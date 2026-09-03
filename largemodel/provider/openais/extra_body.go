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

package openais

import (
	"encoding/json"
	"fmt"

	"github.com/vogo/vage/largemodel/internal/modelcore"
)

// ExtensionNamespace is the ProviderExtensions key this codec reads.
const ExtensionNamespace = "openais"

// reservedExtraBodyKeys are envelope and formal fields an ExtraBody payload
// must not override. aimodel also rejects collisions with modelled wire
// fields at marshal time.
var reservedExtraBodyKeys = map[string]bool{
	"model":                 true,
	"messages":              true,
	"tools":                 true,
	"top_p":                 true,
	"seed":                  true,
	"frequency_penalty":     true,
	"presence_penalty":      true,
	"tool_choice":           true,
	"temperature":           true,
	"max_tokens":            true,
	"max_completion_tokens": true,
	"stop":                  true,
	"response_format":       true,
}

// WithExtraBody builds an openais-namespace extension payload from extra
// top-level JSON fields. Keys that collide with envelope or formal request
// fields, or values that are not JSON, fail here rather than on the wire.
func WithExtraBody(extra map[string]any) (map[string]any, error) {
	if extra == nil {
		return map[string]any{}, nil
	}

	out := make(map[string]any, len(extra))
	for key, value := range extra {
		if key == "" {
			return nil, fmt.Errorf("vage: openais extra body key is empty")
		}

		if reservedExtraBodyKeys[key] {
			return nil, fmt.Errorf("vage: openais extra body key %q collides with a formal request field", key)
		}

		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("vage: openais extra body key %q is not JSON: %w", key, err)
		}

		if !json.Valid(encoded) {
			return nil, fmt.Errorf("vage: openais extra body key %q holds invalid JSON", key)
		}

		out[key] = json.RawMessage(encoded)
	}

	return out, nil
}

func extraBodyFromRequest(req *modelcore.Request) (map[string]json.RawMessage, error) {
	if len(req.ProviderExtensions) == 0 {
		return nil, nil
	}

	for namespace := range req.ProviderExtensions {
		if namespace != ExtensionNamespace {
			return nil, fmt.Errorf("vage: provider extension namespace %q does not match protocol openai-chat", namespace)
		}
	}

	payload := req.ProviderExtensions[ExtensionNamespace]
	if payload == nil {
		return nil, nil
	}

	body, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("vage: openais provider extension must be a JSON object")
	}

	out := make(map[string]json.RawMessage, len(body))
	for key, value := range body {
		if reservedExtraBodyKeys[key] {
			return nil, fmt.Errorf("vage: openais extra body key %q collides with a formal request field", key)
		}

		switch typed := value.(type) {
		case json.RawMessage:
			if !json.Valid(typed) {
				return nil, fmt.Errorf("vage: openais extra body key %q holds invalid JSON", key)
			}

			out[key] = append(json.RawMessage(nil), typed...)
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("vage: openais extra body key %q is not JSON: %w", key, err)
			}

			out[key] = encoded
		}
	}

	return out, nil
}
