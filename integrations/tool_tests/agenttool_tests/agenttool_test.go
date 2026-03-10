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

package agenttool_tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/agent/taskagent"
	"github.com/vogo/vagent/largemodel"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
	"github.com/vogo/vagent/tool/agenttool"
)

// TestAgentAsToolIntegration verifies that a coordinator TaskAgent can delegate
// work to a sub-agent registered as a tool. The coordinator's LLM should
// select the sub-agent tool when prompted, and the coordinator's final response
// should incorporate the sub-agent's output.
//
// Test case: Register a "translator" CustomAgent as a tool on the coordinator.
// The translator always returns a fixed French translation. The coordinator is
// instructed to use the translator tool when asked to translate something.
// We verify the coordinator's response contains the translated text.
//
// Prerequisites: AI_API_KEY, AI_BASE_URL, OPENAI_MODEL environment variables.
func TestAgentAsToolIntegration(t *testing.T) {
	// Create aimodel client from environment variables.
	client, err := aimodel.NewClient(
		aimodel.WithDefaultModel(aimodel.GetEnv("OPENAI_MODEL")),
	)
	if err != nil {
		t.Logf("Failed to create aimodel client (missing API keys?): %v", err)
		return
	}

	// Create a sub-agent that acts as a "translator".
	// It returns a fixed French translation regardless of input.
	translatorAgent := agent.NewCustomAgent(
		agent.Config{
			ID:          "translator-agent",
			Name:        "translate_to_french",
			Description: "Translates the given English text to French. Use this tool when the user asks for a French translation.",
		},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			input := req.Messages[0].Content.Text()
			// Return a fixed translation to make assertions deterministic.
			translation := fmt.Sprintf("Bonjour, le monde! (translated from: %s)", input)
			return &schema.RunResponse{
				Messages: []schema.Message{
					{
						Message: aimodel.Message{
							Role:    aimodel.RoleAssistant,
							Content: aimodel.NewTextContent(translation),
						},
					},
				},
			}, nil
		},
	)

	// Create tool registry and register the sub-agent as a tool.
	reg := tool.NewRegistry()
	if err := agenttool.Register(reg, translatorAgent); err != nil {
		t.Fatalf("RegisterAgentAsTool error: %v", err)
	}

	// Build the largemodel with minimal middleware.
	model := largemodel.New(client,
		largemodel.WithMiddleware(
			largemodel.NewLogMiddleware(),
		),
	)

	// Build the coordinator TaskAgent with the sub-agent tool.
	coordinator := taskagent.New(agent.Config{
		ID:   "coordinator-agent",
		Name: "Coordinator",
	},
		taskagent.WithChatCompleter(model),
		taskagent.WithToolRegistry(reg),
		taskagent.WithSystemPrompt(prompt.StringPrompt(
			"You are a helpful assistant. When the user asks you to translate text to French, "+
				"you MUST use the translate_to_french tool. Pass the text to translate as the 'input' parameter. "+
				"After receiving the tool result, include the translated text in your response.",
		)),
		taskagent.WithMaxIterations(5),
	)

	// Send a request that should trigger the coordinator to use the translator tool.
	resp, err := agent.RunText(context.Background(), coordinator, "Please translate 'Hello, world!' to French.")
	if err != nil {
		t.Fatalf("RunText error: %v", err)
	}

	// Verify the response contains output from the sub-agent.
	if len(resp.Messages) == 0 {
		t.Fatal("expected at least one response message, got none")
	}

	// Collect all assistant message text.
	var responseText strings.Builder
	for _, msg := range resp.Messages {
		if msg.Role == aimodel.RoleAssistant {
			text := msg.Content.Text()
			if text != "" {
				responseText.WriteString(text)
				responseText.WriteString(" ")
			}
		}
	}

	fullResponse := responseText.String()
	t.Logf("Coordinator response: %s", fullResponse)

	// The coordinator's response should contain the sub-agent's translated text.
	// The sub-agent always includes "Bonjour" in its response.
	if !strings.Contains(fullResponse, "Bonjour") {
		t.Errorf("expected coordinator response to contain 'Bonjour' from sub-agent, got: %s", fullResponse)
	}
}

// TestAgentAsToolRegistrationAndListing verifies the tool registry correctly
// tracks agent tools with proper metadata (source, agent ID) without
// requiring LLM API keys.
//
// Test cases:
// - Register a CustomAgent as a tool and verify List() shows ToolSourceAgent and correct AgentID.
// - Register with custom name/description overrides and verify they are reflected.
// - Verify duplicate registration returns an error.
// - Execute the agent tool directly via registry and verify the result.
func TestAgentAsToolRegistrationAndListing(t *testing.T) {
	// Create a simple echo agent.
	echoAgent := agent.NewCustomAgent(
		agent.Config{
			ID:          "echo-id",
			Name:        "echo_agent",
			Description: "Echoes back the input",
		},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			input := req.Messages[0].Content.Text()
			return &schema.RunResponse{
				Messages: []schema.Message{
					{
						Message: aimodel.Message{
							Role:    aimodel.RoleAssistant,
							Content: aimodel.NewTextContent("Echo: " + input),
						},
					},
				},
			}, nil
		},
	)

	t.Run("RegisterAndList", func(t *testing.T) {
		// Register the agent as a tool and verify List() metadata.
		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, echoAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		defs := reg.List()
		if len(defs) != 1 {
			t.Fatalf("expected 1 tool in registry, got %d", len(defs))
		}

		def := defs[0]
		if def.Source != schema.ToolSourceAgent {
			t.Errorf("Source = %q, want %q", def.Source, schema.ToolSourceAgent)
		}

		if def.AgentID != "echo-id" {
			t.Errorf("AgentID = %q, want %q", def.AgentID, "echo-id")
		}

		if def.Name != "echo_agent" {
			t.Errorf("Name = %q, want %q", def.Name, "echo_agent")
		}

		if def.Description != "Echoes back the input" {
			t.Errorf("Description = %q, want %q", def.Description, "Echoes back the input")
		}
	})

	t.Run("CustomNameAndDescription", func(t *testing.T) {
		// Register with overridden name and description.
		reg := tool.NewRegistry()
		err := agenttool.Register(reg, echoAgent,
			agenttool.WithName("custom_echo"),
			agenttool.WithDescription("A custom echo tool"),
		)
		if err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		def, ok := reg.Get("custom_echo")
		if !ok {
			t.Fatal("Get returned false for custom_echo")
		}

		if def.Name != "custom_echo" {
			t.Errorf("Name = %q, want %q", def.Name, "custom_echo")
		}

		if def.Description != "A custom echo tool" {
			t.Errorf("Description = %q, want %q", def.Description, "A custom echo tool")
		}

		// Original name should not be registered.
		if _, ok := reg.Get("echo_agent"); ok {
			t.Error("original name 'echo_agent' should not be registered")
		}
	})

	t.Run("DuplicateRegistrationError", func(t *testing.T) {
		// Register the same agent twice; second should fail.
		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, echoAgent); err != nil {
			t.Fatalf("first RegisterAgentAsTool error: %v", err)
		}

		err := agenttool.Register(reg, echoAgent)
		if err == nil {
			t.Fatal("expected error for duplicate registration, got nil")
		}

		if !strings.Contains(err.Error(), "already registered") {
			t.Errorf("error = %q, want to contain 'already registered'", err.Error())
		}
	})

	t.Run("DirectExecution", func(t *testing.T) {
		// Execute the agent tool directly via the registry and verify the result.
		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, echoAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "echo_agent", `{"input":"integration test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		expected := "Echo: integration test"
		if result.Content[0].Text != expected {
			t.Errorf("result text = %q, want %q", result.Content[0].Text, expected)
		}
	})

	t.Run("MalformedJSONExecution", func(t *testing.T) {
		// Execute with malformed JSON; should return IsError=true.
		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, echoAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "echo_agent", "not valid json")
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for malformed JSON")
		}

		if !strings.HasPrefix(result.Content[0].Text, "agent tool: invalid arguments:") {
			t.Errorf("error text = %q, want prefix 'agent tool: invalid arguments:'", result.Content[0].Text)
		}
	})

	t.Run("MissingInputField", func(t *testing.T) {
		// Execute with valid JSON but missing 'input' field.
		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, echoAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "echo_agent", `{"text":"hello"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for missing input field")
		}

		if result.Content[0].Text != "agent tool: 'input' field must be a non-empty string" {
			t.Errorf("error text = %q, want %q", result.Content[0].Text, "agent tool: 'input' field must be a non-empty string")
		}
	})

	t.Run("AgentErrorExecution", func(t *testing.T) {
		// Register an agent that returns an error from Run.
		errorAgent := agent.NewCustomAgent(
			agent.Config{ID: "err-id", Name: "error_agent", Description: "always fails"},
			func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
				return nil, fmt.Errorf("deliberate failure")
			},
		)

		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, errorAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "error_agent", `{"input":"test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if !result.IsError {
			t.Fatal("expected IsError=true for agent error")
		}

		if !strings.HasPrefix(result.Content[0].Text, "agent tool: execution failed:") {
			t.Errorf("error text = %q, want prefix 'agent tool: execution failed:'", result.Content[0].Text)
		}
	})

	t.Run("EmptyResponseExecution", func(t *testing.T) {
		// Register an agent that returns an empty RunResponse.
		emptyAgent := agent.NewCustomAgent(
			agent.Config{ID: "empty-id", Name: "empty_agent", Description: "returns nothing"},
			func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
				return &schema.RunResponse{}, nil
			},
		)

		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, emptyAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "empty_agent", `{"input":"test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		if result.Content[0].Text != "" {
			t.Errorf("result text = %q, want empty string", result.Content[0].Text)
		}
	})

	t.Run("MultipleAssistantMessages", func(t *testing.T) {
		// Register an agent that returns multiple assistant messages.
		// Verify they are concatenated with newlines.
		multiAgent := agent.NewCustomAgent(
			agent.Config{ID: "multi-id", Name: "multi_agent", Description: "returns multiple messages"},
			func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
				return &schema.RunResponse{
					Messages: []schema.Message{
						{
							Message: aimodel.Message{
								Role:    aimodel.RoleAssistant,
								Content: aimodel.NewTextContent("First line"),
							},
						},
						{
							Message: aimodel.Message{
								Role:    aimodel.RoleUser, // should be filtered out
								Content: aimodel.NewTextContent("User line"),
							},
						},
						{
							Message: aimodel.Message{
								Role:    aimodel.RoleAssistant,
								Content: aimodel.NewTextContent("Second line"),
							},
						},
					},
				}, nil
			},
		)

		reg := tool.NewRegistry()
		if err := agenttool.Register(reg, multiAgent); err != nil {
			t.Fatalf("RegisterAgentAsTool error: %v", err)
		}

		result, err := reg.Execute(context.Background(), "multi_agent", `{"input":"test"}`)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if result.IsError {
			t.Fatalf("expected IsError=false, got true: %s", result.Content[0].Text)
		}

		expected := "First line\nSecond line"
		if result.Content[0].Text != expected {
			t.Errorf("result text = %q, want %q", result.Content[0].Text, expected)
		}
	})
}
