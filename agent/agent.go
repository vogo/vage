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

	"github.com/vogo/vagent/schema"
)

// Agent is the core interface for all agent types.
type Agent interface {
	Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
	ID() string
	Name() string
	Description() string
}

// Config holds the common configuration shared by all agent types.
type Config struct {
	ID          string
	Name        string
	Description string
}

// agentMeta implements ID/Name/Description for embedding into concrete agent types.
type agentMeta struct {
	id          string
	name        string
	description string
}

func newAgentMeta(cfg Config) agentMeta {
	return agentMeta{
		id:          cfg.ID,
		name:        cfg.Name,
		description: cfg.Description,
	}
}

func (m *agentMeta) ID() string          { return m.id }
func (m *agentMeta) Name() string        { return m.name }
func (m *agentMeta) Description() string { return m.description }

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
		Messages: []schema.Message{schema.NewUserMessage(input)},
	})
}

// RunStreamText is a convenience function that sends a single text message and returns
// a streaming response. The agent must implement StreamAgent.
func RunStreamText(ctx context.Context, a StreamAgent, input string) (*schema.RunStream, error) {
	return a.RunStream(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(input)},
	})
}

// RunToStream wraps a non-streaming Agent.Run call as a RunStream,
// emitting AgentStart and AgentEnd lifecycle events.
func RunToStream(ctx context.Context, a Agent, req *schema.RunRequest) *schema.RunStream {
	return schema.NewRunStream(ctx, defaultStreamBufferSize, func(ctx context.Context, send func(schema.Event) error) error {
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
			msg = resp.Messages[0].Content.Text()
		}

		return send(schema.NewEvent(schema.EventAgentEnd, agentID, sessionID, schema.AgentEndData{
			Duration: time.Since(start).Milliseconds(),
			Message:  msg,
		}))
	})
}
