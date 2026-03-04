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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

func main() {
	// Create aimodel client. Reads AI_API_KEY / AI_BASE_URL / AI_MODEL from env.
	client, err := aimodel.NewClient(
		aimodel.WithDefaultModel(aimodel.GetEnv("OPENAI_MODEL")),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Register tools.
	reg := tool.NewRegistry()

	_ = reg.Register(schema.ToolDef{
		Name:        "get_weather",
		Description: "Get current weather for a city",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "The city name, e.g. Paris",
				},
			},
			"required": []string{"city"},
		},
	}, handleGetWeather)

	_ = reg.Register(schema.ToolDef{
		Name:        "calculate",
		Description: "Evaluate a simple math expression",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expression": map[string]any{
					"type":        "string",
					"description": "A math expression, e.g. 2+3*4",
				},
			},
			"required": []string{"expression"},
		},
	}, handleCalculate)

	// Build the LLM agent.
	a := agent.NewLLMAgent(agent.Config{
		ID:   "weather-agent",
		Name: "Weather Assistant",
	},
		agent.WithChatCompleter(client),
		agent.WithToolRegistry(reg),
		agent.WithSystemPrompt(prompt.StringPrompt(
			"You are a helpful assistant. Use tools to answer questions. Be concise.",
		)),
		agent.WithMaxIterations(5),
	)

	question := "What's the weather in Beijing and Tokyo? Also, what is 42 * 17?"

	if len(os.Args) > 1 && os.Args[1] == "--text" {
		// Non-streaming mode if --text flag is passed.
		runText(a, question)
		return
	}

	// Streaming mode
	runStreaming(a, question)
}

func runText(a *agent.LLMAgent, question string) {
	resp, err := agent.RunText(context.Background(), a, question)
	if err != nil {
		log.Fatal(err)
	}

	for _, msg := range resp.Messages {
		fmt.Println(msg.Content.Text())
	}

	fmt.Printf("\n--- Usage: prompt=%d completion=%d total=%d, duration=%dms ---\n",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, resp.Duration)
}

func runStreaming(a *agent.LLMAgent, question string) {
	rs, err := agent.RunStreamText(context.Background(), a, question)
	if err != nil {
		log.Fatal(err)
	}

	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			log.Fatal(recvErr)
		}

		switch data := e.Data.(type) {
		case schema.AgentStartData:
			fmt.Println("[stream] Agent started")
		case schema.TextDeltaData:
			fmt.Print(data.Delta)
		case schema.IterationStartData:
			fmt.Printf("\n[stream] Iteration %d\n", data.Iteration)
		case schema.ToolCallStartData:
			fmt.Printf("\n[stream] Tool call: %s(%s)\n", data.ToolName, data.Arguments)
		case schema.ToolCallEndData:
			fmt.Printf("\n[stream] Tool call %s completed in %dms\n", data.ToolName, data.Duration)
		case schema.ToolResultData:
			fmt.Printf("\n[stream] Tool result: %s\n", toolResultText(data.Result))
		case schema.AgentEndData:
			fmt.Printf("\n[stream] Agent finished in %dms\n", data.Duration)
		}
	}
}

func toolResultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

// handleGetWeather is a mock weather tool handler.
func handleGetWeather(_ context.Context, _, args string) (schema.ToolResult, error) {
	var params struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return schema.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}

	// Return mock weather data.
	temp := 15 + rand.IntN(20)
	result := fmt.Sprintf(`{"city":%q,"temperature":%d,"unit":"°C","condition":"sunny"}`, params.City, temp)

	fmt.Printf("\n[tool] get_weather(%s) -> %s\n", params.City, result)

	return schema.TextResult("", result), nil
}

// handleCalculate is a mock calculator tool handler.
func handleCalculate(_ context.Context, _, args string) (schema.ToolResult, error) {
	var params struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return schema.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}

	// Simple hardcoded evaluation for the demo.
	result := fmt.Sprintf(`{"expression":%q,"result":714}`, params.Expression)

	fmt.Printf("\n[tool] calculate(%s) -> %s\n", params.Expression, result)

	return schema.TextResult("", result), nil
}
