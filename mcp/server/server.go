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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/security/credscrub"
)

// Direction values for credential scan events on the server side.
const (
	DirectionServerInbound  = "mcp_server_inbound"
	DirectionServerOutbound = "mcp_server_outbound"
)

// ScanEvent is delivered to an optional callback when the server-side
// credential scanner detects a hit. Hits carry only masked previews.
type ScanEvent struct {
	Direction string
	ToolName  string
	Hits      []credscrub.Hit
	Action    credscrub.Action
	Truncated bool
}

// ScanCallback receives a scan event. Must be safe for concurrent use.
type ScanCallback func(ctx context.Context, ev ScanEvent)

// Option configures a Server.
type Option func(*Server)

// WithCredentialScanner installs a scanner used to filter handler inputs
// (inbound) and handler outputs (outbound). A nil scanner is a no-op.
func WithCredentialScanner(s *credscrub.Scanner) Option {
	return func(s0 *Server) { s0.scanner = s }
}

// WithScanCallback installs a callback invoked on each scan hit cluster.
func WithScanCallback(cb ScanCallback) Option {
	return func(s *Server) { s.onScan = cb }
}

// ToolRegistration describes a tool to register on the MCP server.
type ToolRegistration struct {
	Name        string
	Description string
	InputSchema any
	Handler     func(ctx context.Context, args map[string]any) (schema.ToolResult, error)
}

// MCPServer is the interface for an MCP protocol server.
type MCPServer interface {
	Serve(ctx context.Context, transport mcp.Transport) error
	RegisterAgent(a agent.Agent) error
	RegisterTool(reg ToolRegistration) error
	Server() *mcp.Server
}

// Server implements MCPServer using the official go-sdk.
type Server struct {
	server  *mcp.Server
	scanner *credscrub.Scanner
	onScan  ScanCallback
	mu      sync.RWMutex
}

// Compile-time check.
var _ MCPServer = (*Server)(nil)

// NewServer creates a new MCP server.
func NewServer(opts ...Option) *Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "vage-mcp-server",
		Version: "1.0.0",
	}, nil)

	s := &Server{server: mcpServer}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Server returns the underlying go-sdk Server for advanced usage.
func (s *Server) Server() *mcp.Server {
	return s.server
}

// Serve runs the server on the given transport (blocking).
func (s *Server) Serve(ctx context.Context, transport mcp.Transport) error {
	return s.server.Run(ctx, transport)
}

// RegisterAgent registers a vage Agent as an MCP tool.
// The tool name is the agent ID and the description is the agent description.
func (s *Server) RegisterAgent(a agent.Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.server.AddTool(&mcp.Tool{
		Name:        a.ID(),
		Description: a.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Input text for the agent",
				},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, blockResp := s.applyInboundScan(ctx, a.ID(), req.Params.Arguments)
		if blockResp != nil {
			return blockResp, nil
		}

		input := ""
		if v, ok := args["input"]; ok {
			input = fmt.Sprintf("%v", v)
		} else if len(req.Params.Arguments) > 0 {
			input = string(req.Params.Arguments)
		}

		runReq := &schema.RunRequest{
			Messages: []schema.Message{schema.NewUserMessage(input)},
		}

		resp, err := a.Run(ctx, runReq)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, nil
		}

		text := ""
		if len(resp.Messages) > 0 {
			text = resp.Messages[0].Content.Text()
		}

		text, outBlock := s.applyOutboundTextScan(ctx, a.ID(), text)
		if outBlock != nil {
			return outBlock, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil
	})

	return nil
}

// RegisterTool registers a custom tool handler on the server.
func (s *Server) RegisterTool(reg ToolRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.server.AddTool(&mcp.Tool{
		Name:        reg.Name,
		Description: reg.Description,
		InputSchema: reg.InputSchema,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, blockResp := s.applyInboundScan(ctx, reg.Name, req.Params.Arguments)
		if blockResp != nil {
			return blockResp, nil
		}

		result, err := reg.Handler(ctx, args)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, nil
		}

		content := make([]mcp.Content, 0, len(result.Content))
		for _, p := range result.Content {
			text := p.Text
			scanned, outBlock := s.applyOutboundTextScan(ctx, reg.Name, text)
			if outBlock != nil {
				return outBlock, nil
			}
			content = append(content, &mcp.TextContent{Text: scanned})
		}

		return &mcp.CallToolResult{
			Content: content,
			IsError: result.IsError,
		}, nil
	})

	return nil
}

// applyInboundScan unmarshals args and runs the scanner on them. Returns
// the (possibly redacted) map. When the scanner returns ActionBlock with
// at least one hit, the second return is a non-nil CallToolResult the
// caller should return immediately.
func (s *Server) applyInboundScan(ctx context.Context, toolName string, raw []byte) (map[string]any, *mcp.CallToolResult) {
	var args map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}
		}
	}

	if s.scanner == nil || args == nil {
		return args, nil
	}

	sr := s.scanner.ScanJSONMap(args)
	if len(sr.Hits) == 0 {
		return args, nil
	}

	s.fireScan(ctx, DirectionServerInbound, toolName, sr)

	if sr.Action == credscrub.ActionBlock {
		return nil, &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{
				Text: "blocked by mcp credential filter (server-in): " + typesSummary(sr.Hits),
			}},
		}
	}

	return args, nil
}

// applyOutboundTextScan scans a handler-produced text. Returns the
// effective text (possibly redacted). When ActionBlock fires, the second
// return is a non-nil CallToolResult the caller should return.
func (s *Server) applyOutboundTextScan(ctx context.Context, toolName, text string) (string, *mcp.CallToolResult) {
	if s.scanner == nil || text == "" {
		return text, nil
	}

	sr := s.scanner.ScanText(text)
	if len(sr.Hits) == 0 {
		return text, nil
	}

	s.fireScan(ctx, DirectionServerOutbound, toolName, sr)

	switch sr.Action {
	case credscrub.ActionBlock:
		return "", &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{
				Text: "blocked by mcp credential filter (server-out): " + typesSummary(sr.Hits),
			}},
		}
	case credscrub.ActionRedact:
		return s.scanner.RedactText(text, sr.Hits), nil
	default:
		return text, nil
	}
}

func (s *Server) fireScan(ctx context.Context, direction, toolName string, sr credscrub.ScanResult) {
	if s.onScan == nil {
		return
	}

	s.onScan(ctx, ScanEvent{
		Direction: direction,
		ToolName:  toolName,
		Hits:      sr.Hits,
		Action:    sr.Action,
		Truncated: sr.Truncated,
	})
}

func typesSummary(hits []credscrub.Hit) string {
	return strings.Join(credscrub.SummarizeTypes(hits), ",")
}
