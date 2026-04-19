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

import "regexp"

// PromptInjectionConfig configures the PromptInjectionGuard.
type PromptInjectionConfig struct {
	Patterns []PatternRule
}

// PromptInjectionGuard detects prompt injection attacks.
// Only checks input direction messages; output messages pass through.
type PromptInjectionGuard struct {
	patterns []PatternRule
}

var _ Guard = (*PromptInjectionGuard)(nil)

// NewPromptInjectionGuard creates a PromptInjectionGuard.
// Panics if any Pattern is nil (programming error).
// The patterns slice is copied defensively.
func NewPromptInjectionGuard(cfg PromptInjectionConfig) *PromptInjectionGuard {
	patterns := make([]PatternRule, len(cfg.Patterns))
	copy(patterns, cfg.Patterns)

	for i, p := range patterns {
		if p.Pattern == nil {
			panic("vage: NewPromptInjectionGuard: pattern " + itoa(i) + " (" + p.Name + ") has nil regexp")
		}
	}

	return &PromptInjectionGuard{patterns: patterns}
}

func (g *PromptInjectionGuard) Name() string { return "prompt_injection" }

func (g *PromptInjectionGuard) Check(msg *Message) (*Result, error) {
	// Prompt injection is only relevant for input messages. Reject any other
	// direction (including DirectionToolResult) so this guard never scans a
	// channel it was not designed for.
	if msg.Direction != DirectionInput {
		return Pass(), nil
	}

	var violations []string

	for _, p := range g.patterns {
		if p.Pattern.MatchString(msg.Content) {
			violations = append(violations, p.Name)
		}
	}

	if len(violations) > 0 {
		return Block(g.Name(), "prompt injection detected", violations...), nil
	}

	return Pass(), nil
}

// DefaultInjectionPatterns returns common prompt injection detection patterns.
// All patterns use (?i) flag for case-insensitive matching.
func DefaultInjectionPatterns() []PatternRule {
	return []PatternRule{
		{Name: "ignore_instructions", Pattern: regexp.MustCompile(`(?i)ignore\s+previous\s+instructions`)},
		{Name: "role_hijack", Pattern: regexp.MustCompile(`(?i)you\s+are\s+now`)},
		{Name: "disregard", Pattern: regexp.MustCompile(`(?i)disregard\s+all`)},
		{Name: "system_prompt", Pattern: regexp.MustCompile(`(?i)system\s+prompt`)},
		{Name: "jailbreak", Pattern: regexp.MustCompile(`(?i)jailbreak`)},
	}
}

// itoa is a simple int-to-string helper to avoid importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var buf [20]byte
	pos := len(buf)
	neg := i < 0

	if neg {
		i = -i
	}

	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}

	if neg {
		pos--
		buf[pos] = '-'
	}

	return string(buf[pos:])
}
