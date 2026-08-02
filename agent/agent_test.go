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
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestInterfaceCompliance(t *testing.T) {
	var _ Agent = (*CustomAgent)(nil)
}

func TestBase(t *testing.T) {
	m := NewBase(Config{ID: "id-1", Name: "name-1", Description: "desc-1"})
	if m.ID() != "id-1" {
		t.Errorf("ID = %q, want %q", m.ID(), "id-1")
	}
	if m.Name() != "name-1" {
		t.Errorf("Name = %q, want %q", m.Name(), "name-1")
	}
	if m.Description() != "desc-1" {
		t.Errorf("Description = %q, want %q", m.Description(), "desc-1")
	}
}

func TestCustomAgent_Config(t *testing.T) {
	a := NewCustomAgent(Config{ID: "c-1", Name: "custom", Description: "custom agent"}, nil)
	if a.ID() != "c-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "c-1")
	}
	if a.Name() != "custom" {
		t.Errorf("Name = %q, want %q", a.Name(), "custom")
	}
	if a.Description() != "custom agent" {
		t.Errorf("Description = %q, want %q", a.Description(), "custom agent")
	}
}

func TestCustomAgent_Run_Delegates(t *testing.T) {
	fn := func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "echo")},
		}, nil
	}
	a := NewCustomAgent(Config{ID: "c-1"}, fn)
	resp, err := a.Run(context.Background(), &schema.RunRequest{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.Messages[0].Text() != "echo" {
		t.Errorf("response = %q, want %q", resp.Messages[0].Text(), "echo")
	}
}

func TestCustomAgent_Run_NilFunc(t *testing.T) {
	a := NewCustomAgent(Config{ID: "c-1"}, nil)
	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error for nil RunFunc")
	}
	if !strings.Contains(err.Error(), "no RunFunc configured") {
		t.Errorf("error = %q, want 'no RunFunc configured'", err.Error())
	}
}
