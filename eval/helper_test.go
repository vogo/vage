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

package eval

import (
	"context"
	"errors"
	"math"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// makeResponse creates a RunResponse with a single assistant message.
func makeResponse(text string) *schema.RunResponse {
	return &schema.RunResponse{
		Messages: []schema.Message{
			{
				Message: schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, text),
			},
		},
	}
}

func makeResponseWithDuration(text string, durationMs int64) *schema.RunResponse {
	resp := makeResponse(text)
	resp.Duration = durationMs

	return resp
}

func makeResponseWithUsage(text string, totalTokens int) *schema.RunResponse {
	resp := makeResponse(text)
	resp.Usage = &schema.Usage{TotalTokens: totalTokens}

	return resp
}

func makeResponseWithToolCalls(calls ...schema.ToolCall) *schema.RunResponse {
	return &schema.RunResponse{
		Messages: []schema.Message{
			{
				Message: schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, ""),
					ToolCalls: calls,
				},
			},
		},
	}
}

func almostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

// mockCompleter implements aimodel.ChatCompleter for testing.
type mockCompleter struct {
	response string
	err      error
}

func (m *mockCompleter) ChatCompletion(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}

	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{
			{
				Message: schema.Message{
					Role:    schema.RoleAssistant,
					Content: aimodel.NewTextContent(m.response),
			},
		},
	}, nil
}

func (m *mockCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented")
}

var errAlwaysFail = errors.New("always fail")
