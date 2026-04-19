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

package guard

import (
	"fmt"
	"regexp"

	"github.com/vogo/vage/schema"
)

// Direction indicates whether a message is input (user->agent) or output (agent->user).
type Direction int

const (
	DirectionInput      Direction = 0
	DirectionOutput     Direction = 1
	DirectionToolResult Direction = 2
)

// Guard checks a message and returns a result.
type Guard interface {
	Check(msg *Message) (*Result, error)
	Name() string
}

// Message carries context for guard checking.
type Message struct {
	Direction Direction
	Content   string
	AgentID   string
	SessionID string
	ToolCalls []schema.ToolResult // populated for output direction
	Metadata  map[string]any
}

// NewInputMessage creates an input direction message.
func NewInputMessage(content string) *Message {
	return &Message{Direction: DirectionInput, Content: content}
}

// NewOutputMessage creates an output direction message.
func NewOutputMessage(content string) *Message {
	return &Message{Direction: DirectionOutput, Content: content}
}

// Action represents the guard check result action type.
type Action string

const (
	ActionPass    Action = "pass"    // Pass, content unchanged
	ActionBlock   Action = "block"   // Block, abort execution
	ActionRewrite Action = "rewrite" // Rewrite, replace content and continue
)

// Result is the outcome of a guard check.
type Result struct {
	Action     Action   // Check result action
	Content    string   // Replacement content when Action=rewrite
	Reason     string   // Reason for block or rewrite
	Violations []string // List of violated rules
	GuardName  string   // Name of the guard that produced this result
}

// Pass returns a Result with ActionPass.
func Pass() *Result {
	return &Result{Action: ActionPass}
}

// Block returns a Result with ActionBlock.
func Block(guardName, reason string, violations ...string) *Result {
	return &Result{
		Action:     ActionBlock,
		GuardName:  guardName,
		Reason:     reason,
		Violations: violations,
	}
}

// Rewrite returns a Result with ActionRewrite.
func Rewrite(guardName, content, reason string, violations ...string) *Result {
	return &Result{
		Action:     ActionRewrite,
		GuardName:  guardName,
		Content:    content,
		Reason:     reason,
		Violations: violations,
	}
}

// BlockedError is returned when a guard blocks a message.
type BlockedError struct {
	Result *Result
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("blocked by %s: %s", e.Result.GuardName, e.Result.Reason)
}

// PatternRule defines a named regex pattern for detection.
// Shared by PromptInjectionGuard and PIIGuard.
type PatternRule struct {
	Name    string
	Pattern *regexp.Regexp
}
