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

package prompt

import (
	"context"
	"testing"
)

func TestStringPrompt_PlainText(t *testing.T) {
	p := StringPrompt("You are a helpful assistant.")
	text, err := p.Render(context.Background(), nil)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if text != "You are a helpful assistant." {
		t.Errorf("Render = %q, want %q", text, "You are a helpful assistant.")
	}
	if p.Name() != "inline" {
		t.Errorf("Name = %q, want %q", p.Name(), "inline")
	}
	if p.Version() != "1" {
		t.Errorf("Version = %q, want %q", p.Version(), "1")
	}
}

func TestStringPrompt_Template(t *testing.T) {
	p := StringPrompt("Hello, {{.Name}}! You are {{.Role}}.")
	text, err := p.Render(context.Background(), map[string]any{
		"Name": "Alice",
		"Role": "an assistant",
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	want := "Hello, Alice! You are an assistant."
	if text != want {
		t.Errorf("Render = %q, want %q", text, want)
	}
}

func TestStringPrompt_TemplateWithNilVars(t *testing.T) {
	// Go text/template renders missing fields on nil data as "<no value>".
	p := StringPrompt("Value: {{.Missing}}")
	text, err := p.Render(context.Background(), nil)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	want := "Value: <no value>"
	if text != want {
		t.Errorf("Render = %q, want %q", text, want)
	}
}

func TestNewPromptTemplate(t *testing.T) {
	p := NewPromptTemplate("system-v2", "2.0", "System: {{.Instructions}}")
	if p.Name() != "system-v2" {
		t.Errorf("Name = %q, want %q", p.Name(), "system-v2")
	}
	if p.Version() != "2.0" {
		t.Errorf("Version = %q, want %q", p.Version(), "2.0")
	}
	text, err := p.Render(context.Background(), map[string]any{
		"Instructions": "Be concise.",
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	want := "System: Be concise."
	if text != want {
		t.Errorf("Render = %q, want %q", text, want)
	}
}

func TestNewPromptTemplate_PlainText(t *testing.T) {
	p := NewPromptTemplate("static", "1.0", "No variables here.")
	text, err := p.Render(context.Background(), nil)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if text != "No variables here." {
		t.Errorf("Render = %q, want %q", text, "No variables here.")
	}
}

func TestStringPrompt_EmptyString(t *testing.T) {
	p := StringPrompt("")
	text, err := p.Render(context.Background(), nil)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if text != "" {
		t.Errorf("Render = %q, want empty", text)
	}
}
