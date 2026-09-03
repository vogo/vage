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

package largemodel

import "fmt"

// ToolChoice is the cross-provider tool-selection policy. Unset (a nil
// Request.ToolChoice) leaves the existing ToolDef.ForceUse compatibility
// behaviour in place. When set, it wins over ForceUse.
type ToolChoice struct {
	Mode ToolChoiceMode
	// Name is the tool to force when Mode is ToolChoiceNamed.
	Name string
}

// ToolChoiceMode is the neutral tool-choice vocabulary.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNamed    ToolChoiceMode = "named"
)

// ToolChoiceAutoValue returns an auto tool-choice.
func ToolChoiceAutoValue() *ToolChoice { return &ToolChoice{Mode: ToolChoiceAuto} }

// ToolChoiceNoneValue returns a none tool-choice.
func ToolChoiceNoneValue() *ToolChoice { return &ToolChoice{Mode: ToolChoiceNone} }

// ToolChoiceRequiredValue returns a required (must call some tool) choice.
func ToolChoiceRequiredValue() *ToolChoice { return &ToolChoice{Mode: ToolChoiceRequired} }

// ToolChoiceNamedValue returns a named tool-choice. name must be a tool
// declared on the same request; the caller is checked before the backend.
func ToolChoiceNamedValue(name string) *ToolChoice {
	return &ToolChoice{Mode: ToolChoiceNamed, Name: name}
}

func (c *ToolChoice) clone() *ToolChoice {
	if c == nil {
		return nil
	}

	out := *c

	return &out
}

func validateToolChoice(req *Request) error {
	if req == nil || req.ToolChoice == nil {
		return nil
	}

	choice := req.ToolChoice
	switch choice.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		if choice.Name != "" {
			return fmt.Errorf("vage: tool_choice mode %q must not set a tool name", choice.Mode)
		}
	case ToolChoiceNamed:
		if choice.Name == "" {
			return fmt.Errorf("vage: named tool_choice requires a tool name")
		}

		for _, def := range req.Tools {
			if def.Name == choice.Name {
				return nil
			}
		}

		return fmt.Errorf("vage: tool_choice names %q, which is not in the request tools", choice.Name)
	default:
		return fmt.Errorf("vage: invalid tool_choice mode %q", choice.Mode)
	}

	return nil
}
