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
	"bytes"
	"context"
	"strings"
	"text/template"
)

// PromptTemplate renders a system prompt with optional variable interpolation.
type PromptTemplate interface {
	Render(ctx context.Context, vars map[string]any) (string, error)
	Name() string
	Version() string
}

// stringPrompt is a simple template backed by text/template.
type stringPrompt struct {
	name    string
	version string
	raw     string
	tmpl    *template.Template // nil if no template delimiters
}

func (s *stringPrompt) Name() string    { return s.name }
func (s *stringPrompt) Version() string { return s.version }

func (s *stringPrompt) Render(_ context.Context, vars map[string]any) (string, error) {
	if s.tmpl == nil {
		return s.raw, nil
	}
	var buf bytes.Buffer
	if err := s.tmpl.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// StringPrompt creates a PromptTemplate from a plain string.
// If the string contains {{ }}, it is parsed as a Go text/template.
func StringPrompt(s string) PromptTemplate {
	sp := &stringPrompt{name: "inline", version: "1", raw: s}
	if strings.Contains(s, "{{") {
		t, err := template.New("prompt").Parse(s)
		if err == nil {
			sp.tmpl = t
		}
	}
	return sp
}

// NewPromptTemplate creates a named, versioned prompt template.
func NewPromptTemplate(name, version, templateStr string) PromptTemplate {
	sp := &stringPrompt{name: name, version: version, raw: templateStr}
	if strings.Contains(templateStr, "{{") {
		t, err := template.New(name).Parse(templateStr)
		if err == nil {
			sp.tmpl = t
		}
	}
	return sp
}
