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

package agent

import (
	"context"
	"time"

	"github.com/vogo/vage/schema"
)

// DefaultStreamBufferSize is the default channel buffer size for streaming events.
const DefaultStreamBufferSize = 32

// Agent is the core interface for all agent types.
type Agent interface {
	Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
	ID() string
	Name() string
	Description() string

	// Protocol reports the vendor wire protocol this agent's model speaks.
	// Messages are stored in vendor-native form, so callers building a
	// request must construct their messages for this protocol.
	Protocol() schema.Protocol
}

// Config holds the common configuration shared by all agent types.
type Config struct {
	ID          string
	Name        string
	Description string

	// Protocol is the vendor wire protocol this agent's model speaks. The
	// zero value means ProtocolOpenAIChat, vage's default.
	Protocol schema.Protocol
}

// Base implements ID/Name/Description/Protocol for embedding into concrete
// agent types.
type Base struct {
	AgentID          string
	AgentName        string
	AgentDescription string
	AgentProtocol    schema.Protocol
}

// NewBase creates a Base from the given Config.
func NewBase(cfg Config) Base {
	proto := cfg.Protocol
	if proto == "" {
		proto = schema.ProtocolOpenAIChat
	}

	return Base{
		AgentID:          cfg.ID,
		AgentName:        cfg.Name,
		AgentDescription: cfg.Description,
		AgentProtocol:    proto,
	}
}

func (m *Base) ID() string          { return m.AgentID }
func (m *Base) Name() string        { return m.AgentName }
func (m *Base) Description() string { return m.AgentDescription }

// Protocol implements Agent.
func (m *Base) Protocol() schema.Protocol { return m.AgentProtocol }

// StreamAgent extends Agent with streaming support.
type StreamAgent interface {
	Agent
	RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error)
}

// StreamMiddleware wraps a send function to intercept, transform, or observe events.
type StreamMiddleware func(next func(schema.Event) error) func(schema.Event) error

// RunFunc is the function signature used by CustomAgent.
type RunFunc func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)

// RunText is a convenience function that sends a single text message to an agent.
func RunText(ctx context.Context, a Agent, input string) (*schema.RunResponse, error) {
	return a.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(a.Protocol(), input)},
	})
}

// RunStreamText is a convenience function that sends a single text message and returns
// a streaming response. The agent must implement StreamAgent.
func RunStreamText(ctx context.Context, a StreamAgent, input string) (*schema.RunStream, error) {
	return a.RunStream(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(a.Protocol(), input)},
	})
}

// RunToStream wraps a non-streaming Agent.Run call as a RunStream,
// emitting AgentStart and AgentEnd lifecycle events.
func RunToStream(ctx context.Context, a Agent, req *schema.RunRequest) *schema.RunStream {
	return schema.NewRunStream(ctx, DefaultStreamBufferSize, func(ctx context.Context, send func(schema.Event) error) error {
		start := time.Now()
		agentID := a.ID()
		sessionID := req.SessionID

		if err := send(schema.NewEvent(schema.EventAgentStart, agentID, sessionID, schema.AgentStartData{})); err != nil {
			return err
		}

		resp, err := a.Run(ctx, req)
		if err != nil {
			return err
		}

		msg := ""
		if len(resp.Messages) > 0 {
			msg = resp.Messages[0].Text()
		}

		return send(schema.NewEvent(schema.EventAgentEnd, agentID, sessionID, schema.AgentEndData{
			Duration: time.Since(start).Milliseconds(),
			Message:  msg,
		}))
	})
}
