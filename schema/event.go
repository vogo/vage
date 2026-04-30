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

package schema

import "time"

// EventType constants for streaming events.
const (
	EventAgentStart     = "agent_start"
	EventTextDelta      = "text_delta"
	EventToolCallStart  = "tool_call_start"
	EventToolCallEnd    = "tool_call_end"
	EventToolResult     = "tool_result"
	EventIterationStart = "iteration_start"
	EventAgentEnd       = "agent_end"
	EventError          = "error"

	// LLM call events (emitted by largemodel metrics middleware).
	EventLLMCallStart = "llm_call_start"
	EventLLMCallEnd   = "llm_call_end"
	EventLLMCallError = "llm_call_error"

	EventTokenBudgetExhausted = "token_budget_exhausted"

	// Session- and daily-level budget events (distinct from EventTokenBudgetExhausted,
	// which is Run-level). Emitted by the BudgetMiddleware host closures — see
	// vv/traces/budgets.
	EventBudgetWarn     = "budget_warn"
	EventBudgetExceeded = "budget_exceeded"

	// Orchestration lifecycle events.
	EventPhaseStart    = "phase_start"
	EventPhaseEnd      = "phase_end"
	EventSubAgentStart = "sub_agent_start"
	EventSubAgentEnd   = "sub_agent_end"

	// Skill lifecycle events.
	EventSkillDiscover     = "skill_discover"
	EventSkillActivate     = "skill_activate"
	EventSkillDeactivate   = "skill_deactivate"
	EventSkillResourceLoad = "skill_resource_load"

	// Interaction events.
	EventPendingInteraction = "pending_interaction"

	// Guard events (emitted when a guard check produces a material outcome
	// such as log/rewrite/block/error; silent passes produce no event).
	EventGuardCheck = "guard_check"

	// MCP credential filter events (emitted by vage/mcp client/server when
	// the credential scanner detects credentials in tool I/O).
	EventMCPCredentialDetected = "mcp_credential_detected"

	// Todo tracking events (emitted by the todo_write built-in tool whenever
	// the session-scoped todo list changes). Payload carries the full snapshot
	// — consumers receive a version number and can treat each event as an
	// idempotent replacement of their local state.
	EventTodoUpdate = "todo_update"

	// Context build event (emitted by vage/context.Builder when a prompt
	// assembly completes). Payload is ContextBuiltData and carries per-source
	// reports for audit and observability.
	EventContextBuilt = "context_built"

	// Workspace events (emitted by the plan_update / notes_write built-in
	// tools whenever the per-session plan workspace changes). Payload is a
	// WorkspacePlanUpdatedData / WorkspaceNoteWrittenData snapshot —
	// consumers can record progress without having to read plan.md.
	EventWorkspacePlanUpdated = "workspace.plan_updated"
	EventWorkspaceNoteWritten = "workspace.note_written"

	// Iteration-level checkpoint event (emitted by TaskAgent at the end
	// of each ReAct iteration after a successful IterationStore.Save).
	// Payload is CheckpointWrittenData. The "successful save" precondition
	// keeps the invariant: hook event count == checkpoints persisted, so
	// downstream consumers can compare against EventIterationStart counts
	// to detect store failures without needing a failure-variant event.
	EventCheckpointWritten = "checkpoint_written"

	// Context editing event (emitted by largemodel.ContextEditorMiddleware
	// when at least one tool_result in an outgoing ChatRequest is folded
	// into a placeholder). Payload is ContextEditedData. Silent passes
	// (nothing eligible / under threshold) emit no event.
	EventContextEdited = "context_edited"

	// Session-tree mutation event (emitted by vage/session/tree store
	// implementations after a successful CreateTree / AddNode / UpdateNode /
	// DeleteNode / SetCursor / DeleteTree). Payload is SessionTreeUpdatedData.
	// Failed writes emit no event so that the consumer-side invariant
	// "events received == successful writes" holds.
	EventSessionTreeUpdated = "session_tree.updated"

	// Session-tree promotion lifecycle events (emitted by vage/session/tree
	// stores when PromoteNode runs). Started fires before the Promoter is
	// invoked, Completed after the new summary has been persisted, and
	// Failed when the Promoter (or the second-phase commit) returns an
	// error. Skipped no-ops (no eligible children) emit no event so the
	// "events received == work performed" invariant holds.
	EventSessionTreePromotionStarted   = "session_tree.promotion.started"
	EventSessionTreePromotionCompleted = "session_tree.promotion.completed"
	EventSessionTreePromotionFailed    = "session_tree.promotion.failed"
)

// EventData is a sealed interface for event payloads.
// Only types within this package may implement it.
type EventData interface {
	eventData() // unexported marker prevents external implementations
}

// Event represents an agent lifecycle event emitted during streaming.
type Event struct {
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Data      EventData `json:"data,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
}

// AgentStartData carries information when the agent begins.
type AgentStartData struct{}

func (AgentStartData) eventData() {}

// TextDeltaData carries a text chunk from the LLM.
type TextDeltaData struct {
	Delta string `json:"delta"`
}

func (TextDeltaData) eventData() {}

// ToolCallStartData carries the start of a tool invocation.
type ToolCallStartData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
}

func (ToolCallStartData) eventData() {}

// ToolCallEndData carries the end of a tool invocation with duration.
type ToolCallEndData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Duration   int64  `json:"duration_ms"`
}

func (ToolCallEndData) eventData() {}

// ToolResultData carries the result of a tool execution.
type ToolResultData struct {
	ToolCallID string     `json:"tool_call_id"`
	ToolName   string     `json:"tool_name"`
	Result     ToolResult `json:"result"`
}

func (ToolResultData) eventData() {}

// IterationStartData carries metadata about a new ReAct loop iteration.
type IterationStartData struct {
	Iteration int `json:"iteration"`
}

func (IterationStartData) eventData() {}

// AgentEndData carries summary information when the agent finishes.
type AgentEndData struct {
	Duration   int64      `json:"duration_ms"`
	Message    string     `json:"message,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`
}

// TokenBudgetExhaustedData carries information when the token budget is exhausted.
type TokenBudgetExhaustedData struct {
	Budget     int  `json:"budget"`
	Used       int  `json:"used"`
	Iterations int  `json:"iterations"`
	Estimated  bool `json:"estimated,omitempty"`
}

func (TokenBudgetExhaustedData) eventData() {}

// BudgetWarnData reports the first crossing of a soft warn threshold on a
// session- or daily-level budget. Emitted at most once per tracker per window.
type BudgetWarnData struct {
	Scope     string  `json:"scope"`     // "session" | "daily"
	Dimension string  `json:"dimension"` // "tokens" | "cost"
	Used      int64   `json:"used"`
	UsedCost  float64 `json:"used_cost,omitempty"`
	Limit     int64   `json:"limit"`
	LimitCost float64 `json:"limit_cost,omitempty"`
	Percent   float64 `json:"percent"`
}

func (BudgetWarnData) eventData() {}

// BudgetExceededData reports that a session- or daily-level hard limit was
// hit and the LLM call was rejected before reaching the network.
type BudgetExceededData struct {
	Scope     string  `json:"scope"`
	Dimension string  `json:"dimension"`
	Used      int64   `json:"used"`
	UsedCost  float64 `json:"used_cost,omitempty"`
	Limit     int64   `json:"limit"`
	LimitCost float64 `json:"limit_cost,omitempty"`
}

func (BudgetExceededData) eventData() {}

func (AgentEndData) eventData() {}

// ErrorData carries error information.
type ErrorData struct {
	Message string `json:"message"`
}

func (ErrorData) eventData() {}

// LLMCallStartData carries information when an LLM call begins.
type LLMCallStartData struct {
	Model    string `json:"model"`
	Messages int    `json:"messages"`
	Tools    int    `json:"tools"`
	Stream   bool   `json:"stream"`
}

func (LLMCallStartData) eventData() {}

// LLMCallEndData carries information when an LLM call completes.
type LLMCallEndData struct {
	Model            string `json:"model"`
	Duration         int64  `json:"duration_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	Stream           bool   `json:"stream"`
}

func (LLMCallEndData) eventData() {}

// LLMCallErrorData carries information when an LLM call fails.
type LLMCallErrorData struct {
	Model    string `json:"model"`
	Duration int64  `json:"duration_ms"`
	Error    string `json:"error"`
	Stream   bool   `json:"stream"`
}

func (LLMCallErrorData) eventData() {}

// PhaseStartData carries information when an orchestration phase begins.
type PhaseStartData struct {
	Phase      string `json:"phase"`       // e.g. "explore", "plan", "dispatch"
	PhaseIndex int    `json:"phase_index"` // 1-based index
	TotalPhase int    `json:"total_phase"` // total number of phases
}

func (PhaseStartData) eventData() {}

// PhaseEndData carries information when an orchestration phase completes.
type PhaseEndData struct {
	Phase            string `json:"phase"`
	Duration         int64  `json:"duration_ms"`
	Summary          string `json:"summary,omitempty"`           // optional phase summary (e.g., plan overview)
	ToolCalls        int    `json:"tool_calls,omitempty"`        // total tool calls in the phase
	PromptTokens     int    `json:"prompt_tokens,omitempty"`     // total prompt tokens in the phase
	CompletionTokens int    `json:"completion_tokens,omitempty"` // total completion tokens in the phase
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"` // total cache-read tokens in the phase
}

func (PhaseEndData) eventData() {}

// SubAgentStartData carries information when a sub-agent begins execution.
type SubAgentStartData struct {
	AgentName   string `json:"agent_name"`
	StepID      string `json:"step_id,omitempty"`     // for plan mode
	Description string `json:"description,omitempty"` // step description
	StepIndex   int    `json:"step_index,omitempty"`  // 1-based step index
	TotalSteps  int    `json:"total_steps,omitempty"` // total steps in plan
}

func (SubAgentStartData) eventData() {}

// SubAgentEndData carries information when a sub-agent finishes execution.
type SubAgentEndData struct {
	AgentName        string `json:"agent_name"`
	StepID           string `json:"step_id,omitempty"`
	Duration         int64  `json:"duration_ms"`
	ToolCalls        int    `json:"tool_calls"`
	TokensUsed       int    `json:"tokens_used"`                 // kept for backward compat (prompt + completion)
	PromptTokens     int    `json:"prompt_tokens,omitempty"`     // prompt tokens used by this sub-agent
	CompletionTokens int    `json:"completion_tokens,omitempty"` // completion tokens used by this sub-agent
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"` // cache-read tokens used by this sub-agent
}

func (SubAgentEndData) eventData() {}

// SkillDiscoverData carries information about skill discovery.
type SkillDiscoverData struct {
	Directory string `json:"directory"`
	Count     int    `json:"count"`
}

func (SkillDiscoverData) eventData() {}

// SkillActivateData carries information when a skill is activated.
type SkillActivateData struct {
	SkillName string `json:"skill_name"`
	SessionID string `json:"session_id"`
}

func (SkillActivateData) eventData() {}

// SkillDeactivateData carries information when a skill is deactivated.
type SkillDeactivateData struct {
	SkillName string `json:"skill_name"`
	SessionID string `json:"session_id"`
}

func (SkillDeactivateData) eventData() {}

// SkillResourceLoadData carries information when a skill resource is loaded.
type SkillResourceLoadData struct {
	SkillName    string `json:"skill_name"`
	ResourceType string `json:"resource_type"`
	ResourceName string `json:"resource_name"`
}

func (SkillResourceLoadData) eventData() {}

// GuardCheckData carries the outcome of a guard check with a material effect
// (log / rewrite / block / error). Silent passes do not emit this event.
type GuardCheckData struct {
	GuardName  string   `json:"guard_name"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	ToolName   string   `json:"tool_name,omitempty"`
	Action     string   `json:"action"`              // "log" | "rewrite" | "block" | "error"
	RuleHits   []string `json:"rule_hits,omitempty"` // matched rule names
	Severity   string   `json:"severity,omitempty"`  // max severity among hits
	Snippet    string   `json:"snippet,omitempty"`   // leading chars of scanned content
}

func (GuardCheckData) eventData() {}

// MCPCredentialDetectedData carries a summary of a credential scan at the
// MCP boundary. Plaintext credentials never appear in this payload — only
// masked previews such as "AKIA****".
type MCPCredentialDetectedData struct {
	Direction string   `json:"direction"` // e.g. "mcp_outbound", "mcp_inbound"
	ServerURI string   `json:"server_uri,omitempty"`
	ToolName  string   `json:"tool_name,omitempty"`
	Action    string   `json:"action"` // "log" | "redact" | "block"
	HitTypes  []string `json:"hit_types"`
	HitCount  int      `json:"hit_count"`
	Masked    []string `json:"masked,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (MCPCredentialDetectedData) eventData() {}

// PendingInteractionData carries information about a pending user interaction.
type PendingInteractionData struct {
	InteractionID  string `json:"interaction_id"`
	Question       string `json:"question"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (PendingInteractionData) eventData() {}

// TodoItem is the wire form of a single todo list entry emitted on
// EventTodoUpdate. It duplicates vage/tool/todo.Item to keep the schema
// package free of a reverse dependency on vage/tool.
type TodoItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ActiveForm string `json:"active_form"`
	Status     string `json:"status"`
}

// TodoUpdateData carries a full snapshot of the session-scoped todo list after
// a successful todo_write invocation. Version is strictly monotonic per
// session; clients may use it as an idempotency key.
type TodoUpdateData struct {
	Version int64      `json:"version"`
	Items   []TodoItem `json:"items"`
}

func (TodoUpdateData) eventData() {}

// ContextSourceReport describes a single Source.Fetch outcome inside a
// Builder run. The vage/context package reuses this type as its FetchReport
// so the wire format and the in-process structure match exactly — no
// mirrored types, no ToEventData copy field-by-field for the source list.
type ContextSourceReport struct {
	Source        string `json:"source"`
	Status        string `json:"status"`                   // "ok" | "skipped" | "error" | "truncated"
	InputN        int    `json:"input_n,omitempty"`        // candidate item count (semantics per source)
	OutputN       int    `json:"output_n"`                 // emitted message count
	DroppedN      int    `json:"dropped_n,omitempty"`      // candidates dropped by the source
	Tokens        int    `json:"tokens"`                   // estimated tokens of emitted messages
	OriginalCount int    `json:"original_count,omitempty"` // session-history sources: pre-compression message count
	Note          string `json:"note,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ContextBuiltData is the payload for EventContextBuilt.
type ContextBuiltData struct {
	Builder      string                `json:"builder"`
	Strategy     string                `json:"strategy"`     // currently fixed at "ordered_greedy"
	BudgetTotal  int                   `json:"budget_total"` // 0 = unlimited
	OutputCount  int                   `json:"output_count"`
	OutputTokens int                   `json:"output_tokens"`
	DroppedCount int                   `json:"dropped_count"`
	Sources      []ContextSourceReport `json:"sources"`
	Duration     int64                 `json:"duration_ms"`
}

func (ContextBuiltData) eventData() {}

// WorkspacePlanUpdatedData is the payload for EventWorkspacePlanUpdated.
// Cleared is true when the writer passed an empty content (deleting plan.md).
type WorkspacePlanUpdatedData struct {
	SessionID string `json:"session_id"`
	Bytes     int    `json:"bytes"`
	Cleared   bool   `json:"cleared,omitempty"`
}

func (WorkspacePlanUpdatedData) eventData() {}

// WorkspaceNoteWrittenData is the payload for EventWorkspaceNoteWritten.
// Cleared is true when the writer passed an empty content (deleting the note).
type WorkspaceNoteWrittenData struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Bytes     int    `json:"bytes"`
	Cleared   bool   `json:"cleared,omitempty"`
}

func (WorkspaceNoteWrittenData) eventData() {}

// CheckpointWrittenData is the payload for EventCheckpointWritten. It
// is emitted by TaskAgent after the IterationStore has successfully
// persisted the iteration snapshot. Final == true ⇒ StopReason != "".
type CheckpointWrittenData struct {
	CheckpointID  string     `json:"checkpoint_id"`
	Sequence      int        `json:"sequence"`
	Iteration     int        `json:"iteration"`
	Final         bool       `json:"final,omitempty"`
	StopReason    StopReason `json:"stop_reason,omitempty"`
	MessagesCount int        `json:"messages_count"`
	TotalTokens   int        `json:"total_tokens"`
}

func (CheckpointWrittenData) eventData() {}

// ContextEditedData is the payload for EventContextEdited. It is
// emitted by largemodel.ContextEditorMiddleware after a successful
// edit pass on a single outgoing ChatRequest. Edited is always >= 1
// (silent passes emit no event).
type ContextEditedData struct {
	Edited        int    `json:"edited"`                      // count of tool_result messages elided this pass
	Kept          int    `json:"kept"`                        // count of tool_result messages kept verbatim
	Total         int    `json:"total"`                       // total messages in the request
	OriginalBytes int    `json:"original_bytes"`              // sum of elided original Content.Text() byte length
	Placeholder   int    `json:"placeholder_bytes,omitempty"` // sum of placeholder string byte length
	Strategy      string `json:"strategy"`                    // currently fixed at "keep_last_k"
}

func (ContextEditedData) eventData() {}

// SessionTreeOperation enumerates the kinds of mutation that can produce
// an EventSessionTreeUpdated. Constants live on the schema side so trace
// readers and SessionHook subscribers can switch on them without importing
// vage/session/tree.
const (
	SessionTreeOpCreate     = "create"      // CreateTree completed
	SessionTreeOpAdd        = "add"         // AddNode completed
	SessionTreeOpUpdate     = "update"      // UpdateNode completed
	SessionTreeOpDelete     = "delete"      // DeleteNode completed
	SessionTreeOpCursor     = "cursor"      // SetCursor completed
	SessionTreeOpDeleteTree = "delete_tree" // DeleteTree completed
)

// SessionTreeUpdatedData is the payload for EventSessionTreeUpdated. It is
// emitted by vage/session/tree.SessionTreeStore implementations after a
// mutation succeeds. NodeID/NodeType/Status are populated for node-level
// operations and left empty on tree-level operations (create/delete_tree).
type SessionTreeUpdatedData struct {
	SessionID string `json:"session_id"`
	Operation string `json:"operation"`
	NodeID    string `json:"node_id,omitempty"`
	NodeType  string `json:"node_type,omitempty"`
	Status    string `json:"status,omitempty"`
	NodeCount int    `json:"node_count"`
}

func (SessionTreeUpdatedData) eventData() {}

// SessionTreePromotionStartedData is the payload for
// EventSessionTreePromotionStarted. Eligible carries the pre-flight count
// of children that the Promoter is about to compress; the actual folded
// count (which can be lower due to interleaving writes) ships in
// SessionTreePromotionCompletedData.
type SessionTreePromotionStartedData struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	Eligible  int    `json:"eligible"`
}

func (SessionTreePromotionStartedData) eventData() {}

// SessionTreePromotionCompletedData is the payload for
// EventSessionTreePromotionCompleted. FoldedCount is the number of
// children whose Promoted field flipped to true on this run;
// NewSummaryBytes is the byte length of the parent's new summary AFTER
// store-side clamping.
type SessionTreePromotionCompletedData struct {
	SessionID       string `json:"session_id"`
	ParentID        string `json:"parent_id"`
	FoldedCount     int    `json:"folded_count"`
	NewSummaryBytes int    `json:"new_summary_bytes"`
}

func (SessionTreePromotionCompletedData) eventData() {}

// SessionTreePromotionFailedData is the payload for
// EventSessionTreePromotionFailed. Error carries the formatted error
// string; structured errors (errors.Is targets) stay in the synchronous
// PromoteNode return value.
type SessionTreePromotionFailedData struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	Error     string `json:"error"`
}

func (SessionTreePromotionFailedData) eventData() {}

// NewEvent creates an Event with the given type, agent ID, session ID, and data.
func NewEvent(eventType, agentID, sessionID string, data EventData) Event {
	return Event{
		Type:      eventType,
		AgentID:   agentID,
		SessionID: sessionID,
		Timestamp: time.Now(),
		Data:      data,
	}
}
