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

package websearch

import (
	"encoding/json"
	"time"

	"github.com/vogo/vage/schema"
)

type searchEnvelope struct {
	Query       string   `json:"query"`
	Provider    string   `json:"provider"`
	Results     []Result `json:"results"`
	RetrievedAt string   `json:"retrieved_at"`
	Warnings    []string `json:"warnings,omitempty"`
	ErrorCode   string   `json:"error_code,omitempty"`
	Message     string   `json:"message,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
}

func jsonResult(env searchEnvelope, isError bool) schema.ToolResult {
	if env.Results == nil {
		env.Results = []Result{}
	}
	if env.RetrievedAt == "" {
		env.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	}
	data, _ := json.MarshalIndent(env, "", "  ")
	return schema.ToolResult{
		Content: []schema.ContentPart{{Type: "text", Text: string(data)}},
		IsError: isError,
	}
}

func errorResult(query, providerName, code, message string) schema.ToolResult {
	return jsonResult(searchEnvelope{
		Query:     query,
		Provider:  providerName,
		ErrorCode: code,
		Message:   message,
	}, true)
}
