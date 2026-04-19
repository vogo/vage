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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/security/credscrub"
	"github.com/vogo/vage/tool"
)

// Direction values for credential scan events.
const (
	DirectionOutbound = "mcp_outbound"
	DirectionInbound  = "mcp_inbound"
)

// ScanEvent is delivered to an optional callback when the credential
// scanner detects a hit on MCP I/O. The hits carry only masked previews,
// never plaintext credentials.
type ScanEvent struct {
	Direction string
	ServerURI string
	ToolName  string
	Hits      []credscrub.Hit
	Action    credscrub.Action
	Truncated bool
}

// ScanCallback receives a scan event. Must be safe for concurrent use.
type ScanCallback func(ctx context.Context, ev ScanEvent)

// Option configures a Client.
type Option func(*Client)

// WithCredentialScanner installs a scanner used to filter tool arguments
// (outbound) and tool results (inbound). A nil scanner is a no-op.
func WithCredentialScanner(s *credscrub.Scanner) Option {
	return func(c *Client) { c.scanner = s }
}

// WithScanCallback installs a callback invoked once per scan hit cluster.
// nil disables the callback.
func WithScanCallback(cb ScanCallback) Option {
	return func(c *Client) { c.onScan = cb }
}

// Lifecycle manages the connection lifecycle of an MCP client.
type Lifecycle interface {
	Connect(ctx context.Context, transport mcp.Transport) error
	Disconnect() error
	Ping(ctx context.Context) error
}

// MCPClient combines tool calling with connection lifecycle management.
type MCPClient interface {
	tool.ExternalToolCaller
	Lifecycle
	ListTools(ctx context.Context) ([]schema.ToolDef, error)
}

// Client implements MCPClient using the official go-sdk.
type Client struct {
	client    *mcp.Client
	session   *mcp.ClientSession
	serverURI string
	scanner   *credscrub.Scanner
	onScan    ScanCallback
	mu        sync.RWMutex
}

// Compile-time check.
var _ MCPClient = (*Client)(nil)

// NewClient creates a new MCP client.
func NewClient(serverURI string, opts ...Option) *Client {
	c := mcp.NewClient(&mcp.Implementation{
		Name:    "vage-mcp-client",
		Version: "1.0.0",
	}, nil)

	cli := &Client{
		client:    c,
		serverURI: serverURI,
	}
	for _, opt := range opts {
		opt(cli)
	}

	return cli
}

// Connect establishes a connection to the server via the given transport.
func (c *Client) Connect(ctx context.Context, transport mcp.Transport) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	session, err := c.client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	c.session = session
	return nil
}

// Disconnect closes the session.
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session != nil {
		err := c.session.Close()
		c.session = nil
		return err
	}
	return nil
}

// ListTools sends tools/list and converts the response to schema.ToolDef slice.
func (c *Client) ListTools(ctx context.Context) ([]schema.ToolDef, error) {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()

	if s == nil {
		return nil, fmt.Errorf("not connected")
	}

	result, err := s.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	defs := make([]schema.ToolDef, len(result.Tools))
	for i, t := range result.Tools {
		defs[i] = schema.ToolDef{
			Name:         t.Name,
			Description:  t.Description,
			Parameters:   t.InputSchema,
			Source:       schema.ToolSourceMCP,
			MCPServerURI: c.serverURI,
		}
	}
	return defs, nil
}

// CallTool sends tools/call and converts the response to schema.ToolResult.
// When a credential scanner is installed, it is applied to both outbound
// arguments and inbound tool-result text.
func (c *Client) CallTool(ctx context.Context, name, args string) (schema.ToolResult, error) {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()

	if s == nil {
		return schema.ToolResult{}, fmt.Errorf("not connected")
	}

	effectiveArgs := args
	if c.scanner != nil {
		sr, redacted, err := c.scanner.ScanJSON([]byte(args))
		if err != nil {
			return schema.ToolResult{}, fmt.Errorf("credential scan failed: %w", err)
		}
		if len(sr.Hits) > 0 {
			c.fireScan(ctx, DirectionOutbound, name, sr)
			switch sr.Action {
			case credscrub.ActionBlock:
				return schema.ErrorResult("",
					"blocked by mcp credential filter (outbound): "+typesSummary(sr.Hits)), nil
			case credscrub.ActionRedact:
				if redacted != nil {
					effectiveArgs = string(redacted)
				}
			case credscrub.ActionLog:
				// leave effectiveArgs as-is
			}
		}
	}

	var argsObj any
	if err := json.Unmarshal([]byte(effectiveArgs), &argsObj); err != nil {
		return schema.ToolResult{}, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	result, err := s.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: argsObj,
	})
	if err != nil {
		return schema.ToolResult{}, fmt.Errorf("call tool: %w", err)
	}

	parts := make([]schema.ContentPart, len(result.Content))
	for i, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			parts[i] = schema.ContentPart{Type: "text", Text: tc.Text}
		}
	}

	if c.scanner != nil {
		for i := range parts {
			if parts[i].Type != "text" || parts[i].Text == "" {
				continue
			}
			sr := c.scanner.ScanText(parts[i].Text)
			if len(sr.Hits) == 0 {
				continue
			}
			c.fireScan(ctx, DirectionInbound, name, sr)
			switch sr.Action {
			case credscrub.ActionBlock:
				return schema.ErrorResult("",
					"blocked by mcp credential filter (inbound): "+typesSummary(sr.Hits)), nil
			case credscrub.ActionRedact:
				parts[i].Text = c.scanner.RedactText(parts[i].Text, sr.Hits)
			case credscrub.ActionLog:
				// leave as-is
			}
		}
	}

	return schema.ToolResult{
		Content: parts,
		IsError: result.IsError,
	}, nil
}

// Ping sends a ping request to the server.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()

	if s == nil {
		return fmt.Errorf("not connected")
	}

	return s.Ping(ctx, &mcp.PingParams{})
}

func (c *Client) fireScan(ctx context.Context, direction, toolName string, sr credscrub.ScanResult) {
	if c.onScan == nil {
		return
	}

	c.onScan(ctx, ScanEvent{
		Direction: direction,
		ServerURI: c.serverURI,
		ToolName:  toolName,
		Hits:      sr.Hits,
		Action:    sr.Action,
		Truncated: sr.Truncated,
	})
}

func typesSummary(hits []credscrub.Hit) string {
	return strings.Join(credscrub.SummarizeTypes(hits), ",")
}
