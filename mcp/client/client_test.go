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
	"testing"

	"github.com/vogo/vage/security/credscrub"
)

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:8080")
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	if c.serverURI != "http://localhost:8080" {
		t.Errorf("expected serverURI %q, got %q", "http://localhost:8080", c.serverURI)
	}

	if c.client == nil {
		t.Error("expected non-nil internal client")
	}

	if c.session != nil {
		t.Error("expected nil session before connect")
	}

	if c.scanner != nil {
		t.Error("expected nil scanner when no option provided")
	}
}

func TestClient_InterfaceCompliance(t *testing.T) {
	var _ MCPClient = (*Client)(nil)
}

func TestClient_DisconnectBeforeConnect(t *testing.T) {
	c := NewClient("http://localhost:8080")

	err := c.Disconnect()
	if err != nil {
		t.Errorf("expected no error disconnecting before connect, got: %v", err)
	}
}

func TestClient_ListToolsNotConnected(t *testing.T) {
	c := NewClient("http://localhost:8080")

	_, err := c.ListTools(t.Context())
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestClient_CallToolNotConnected(t *testing.T) {
	c := NewClient("http://localhost:8080")

	_, err := c.CallTool(t.Context(), "test", `{"key":"value"}`)
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestClient_PingNotConnected(t *testing.T) {
	c := NewClient("http://localhost:8080")

	err := c.Ping(t.Context())
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestClient_WithCredentialScanner(t *testing.T) {
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})
	c := NewClient("http://localhost:8080", WithCredentialScanner(s))

	if c.scanner != s {
		t.Error("expected scanner to be installed by option")
	}
}

func TestClient_WithScanCallback(t *testing.T) {
	called := false
	cb := func(_ context.Context, _ ScanEvent) { called = true }
	c := NewClient("http://localhost:8080", WithScanCallback(cb))

	if c.onScan == nil {
		t.Error("expected callback to be installed")
	}

	// Invoke synthetically to confirm the stored callback is the one we passed.
	c.onScan(t.Context(), ScanEvent{Direction: DirectionOutbound})
	if !called {
		t.Error("stored callback did not fire")
	}
}

func TestClient_CallToolBlockOutbound_NoSession(t *testing.T) {
	// The outbound block path runs the scan BEFORE checking the session,
	// since the scan may return a ToolResult without touching the transport.
	// But the wiring is: scan runs after the session nil-check, so this
	// test confirms the nil-check gate still works when a scanner is
	// installed and args contain credentials.
	s := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})
	c := NewClient("http://localhost:8080", WithCredentialScanner(s))

	_, err := c.CallTool(t.Context(), "test", `{"password":"hunter2"}`)
	if err == nil {
		t.Error("expected not-connected error even with scanner installed")
	}
}

func TestTypesSummary(t *testing.T) {
	hits := []credscrub.Hit{
		{Type: "aws_access_key"},
		{Type: "github_token"},
		{Type: "aws_access_key"},
	}

	got := typesSummary(hits)
	if got != "aws_access_key,github_token" {
		t.Errorf("want sorted comma-joined types; got %q", got)
	}
}
