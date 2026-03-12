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
	"testing"
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
