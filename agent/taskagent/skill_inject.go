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

package taskagent

import (
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/tool"
)

// prepareAITools converts registry tools to aimodel.Tool slice, applying any filter.
func (a *Agent) prepareAITools(filter []string) []aimodel.Tool {
	if a.toolRegistry == nil {
		return nil
	}

	defs := a.toolRegistry.List()
	defs = tool.FilterTools(defs, filter)
	return tool.ToAIModelTools(defs)
}

// mergeSkillToolFilter merges skill AllowedTools with the request-level tool filter.
// If any active skill does not declare AllowedTools (meaning it has no restriction),
// the result is the requestFilter as-is (no additional filtering).
// Only when ALL active skills that declare AllowedTools is the union used as a filter.
func (a *Agent) mergeSkillToolFilter(requestFilter []string, sessionID string) []string {
	if a.skillManager == nil {
		return requestFilter
	}

	active := a.skillManager.ActiveSkills(sessionID)
	if len(active) == 0 {
		return requestFilter
	}

	// Collect union of all skill allowed tools.
	// If any active skill does NOT declare AllowedTools, it means "unrestricted",
	// so we skip skill-level filtering entirely.
	var skillTools []string
	seen := make(map[string]bool)

	for _, act := range active {
		def := act.SkillDef()
		if len(def.AllowedTools) == 0 {
			// This skill has no tool restriction — don't filter.
			return requestFilter
		}
		for _, t := range def.AllowedTools {
			if !seen[t] {
				seen[t] = true
				skillTools = append(skillTools, t)
			}
		}
	}

	// If no request filter, use skill tools only.
	if len(requestFilter) == 0 {
		return skillTools
	}

	// Intersect skill tools with request filter.
	reqSet := make(map[string]bool, len(requestFilter))
	for _, t := range requestFilter {
		reqSet[t] = true
	}

	var result []string
	for _, t := range skillTools {
		if reqSet[t] {
			result = append(result, t)
		}
	}

	return result
}

// injectSkillInstructions appends active skill instructions to the system prompt.
func (a *Agent) injectSkillInstructions(br *buildResult, sessionID string) {
	if a.skillManager == nil {
		return
	}

	active := a.skillManager.ActiveSkills(sessionID)
	if len(active) == 0 {
		return
	}

	var sb strings.Builder
	for _, act := range active {
		def := act.SkillDef()
		if def.Instructions == "" {
			continue
		}
		sb.WriteString("\n<skill name=\"")
		sb.WriteString(act.SkillName)
		sb.WriteString("\">\n")
		sb.WriteString(def.Instructions)
		sb.WriteString("\n</skill>")
	}

	if sb.Len() == 0 {
		return
	}

	skillText := sb.String()

	// If there is a system message, append to it; otherwise prepend a new system message.
	if len(br.messages) > 0 && br.messages[0].Role == aimodel.RoleSystem {
		existing := br.messages[0].Content.Text()
		br.messages[0].Content = aimodel.NewTextContent(existing + skillText)
	} else {
		sysMsg := aimodel.Message{
			Role:    aimodel.RoleSystem,
			Content: aimodel.NewTextContent(skillText),
		}
		br.messages = append([]aimodel.Message{sysMsg}, br.messages...)
	}
}

// markPromptCacheBreakpoints attaches cache-breakpoint hints to the two
// stable per-session surfaces: the last system message (if any) and the
// last tool definition (if any). Messages and tools are slice-backed, so
// mutating in place propagates to every ReAct iteration that reuses the
// slice for the outgoing ChatRequest.
func markPromptCacheBreakpoints(messages []aimodel.Message, tools []aimodel.Tool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == aimodel.RoleSystem {
			messages[i].CacheBreakpoint = true
			break
		}
	}
	if len(tools) > 0 {
		tools[len(tools)-1].CacheBreakpoint = true
	}
}
