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

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// prepareAITools returns the registry's tool definitions, applying any filter.
// Empty names with the historical FilterTools helper means "no request-level
// restriction" (all tools). Frozen empty sets must go through
// prepareFrozenAITools instead.
func (a *Agent) prepareAITools(filter []string) []schema.ToolDef {
	if a.toolRegistry == nil {
		return nil
	}

	return tool.FilterTools(a.toolRegistry.List(), filter)
}

// prepareFrozenAITools selects tools by a post-intersection name list.
// An empty list is fail-closed: no tools, never a fallback to the full set.
func (a *Agent) prepareFrozenAITools(names []string) []schema.ToolDef {
	if a.toolRegistry == nil || len(names) == 0 {
		return nil
	}

	return tool.FilterTools(a.toolRegistry.List(), names)
}

// toolsForRun returns the tool definitions this invocation may expose.
// Fresh runs freeze names in preflight; resume-from-interrupt consumes the
// snapshot as-is. Checkpoint Resume keeps the historical unfrozen merge.
func (a *Agent) toolsForRun(p runParams, sessionID string) []schema.ToolDef {
	if p.toolsFrozen {
		return a.prepareFrozenAITools(p.toolFilter)
	}
	return a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, sessionID))
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
	if len(br.messages) > 0 && br.messages[0].Role() == schema.RoleSystem {
		br.messages[0].SetText(br.messages[0].Text() + skillText)
	} else {
		sysMsg := schema.NewSystemMessage(schema.ProtocolOf(br.messages), skillText)
		br.messages = append([]schema.Message{sysMsg}, br.messages...)
	}
}
