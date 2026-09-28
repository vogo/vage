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

// Package schema is vage's view of the canonical model-contract layer.
//
// The definitions now live in [github.com/vogo/largemodel/schema]: the canonical
// Message / Event / Usage vocabulary is a property of talking to a large model,
// not of the agent framework, so it belongs to the module that owns model access.
// This package re-exports that layer unchanged so existing vage and vv code —
// and vage's own public API — keep compiling against a single set of types.
//
// Every declaration here is an alias, never a defined type: schema.Message and
// largemodel/schema.Message are the same type, so a value crosses the boundary
// without conversion and neither side can drift from the other.
package schema

import largemodelschema "github.com/vogo/largemodel/schema"

type AgentEndData = largemodelschema.AgentEndData

type AgentStartData = largemodelschema.AgentStartData

type BudgetExceededData = largemodelschema.BudgetExceededData

type BudgetWarnData = largemodelschema.BudgetWarnData

type CheckpointWrittenData = largemodelschema.CheckpointWrittenData

type ContentPart = largemodelschema.ContentPart

type ContextBuiltData = largemodelschema.ContextBuiltData

type ContextEditedData = largemodelschema.ContextEditedData

type ContextSourceReport = largemodelschema.ContextSourceReport

type CustomEventData = largemodelschema.CustomEventData

type Emitter = largemodelschema.Emitter

type ErrorData = largemodelschema.ErrorData

type Event = largemodelschema.Event

type EventAccumulator = largemodelschema.EventAccumulator

type EventData = largemodelschema.EventData

type EventDispatcher = largemodelschema.EventDispatcher

type GuardCheckData = largemodelschema.GuardCheckData

type InterruptCreatedData = largemodelschema.InterruptCreatedData

type InterruptDecision = largemodelschema.InterruptDecision

type InterruptDecisionStoredData = largemodelschema.InterruptDecisionStoredData

type InterruptDescriptor = largemodelschema.InterruptDescriptor

type InterruptResumedData = largemodelschema.InterruptResumedData

type IterationStartData = largemodelschema.IterationStartData

type LLMCallEndData = largemodelschema.LLMCallEndData

type LLMCallErrorData = largemodelschema.LLMCallErrorData

type LLMCallStartData = largemodelschema.LLMCallStartData

type MCPCredentialDetectedData = largemodelschema.MCPCredentialDetectedData

type Message = largemodelschema.Message

type MessagePart = largemodelschema.MessagePart

type MessagePartType = largemodelschema.MessagePartType

type ParamsResolvedData = largemodelschema.ParamsResolvedData

type PendingInteractionData = largemodelschema.PendingInteractionData

type PhaseEndData = largemodelschema.PhaseEndData

type PhaseStartData = largemodelschema.PhaseStartData

type Protocol = largemodelschema.Protocol

type ResourceMode = largemodelschema.ResourceMode

type ResourceRef = largemodelschema.ResourceRef

type ResourceTracker = largemodelschema.ResourceTracker

type ResumeInterruptRequest = largemodelschema.ResumeInterruptRequest

type Role = largemodelschema.Role

type RouteSelectedData = largemodelschema.RouteSelectedData

type RunLimits = largemodelschema.RunLimits

type RunOptions = largemodelschema.RunOptions

type RunRequest = largemodelschema.RunRequest

type RunResponse = largemodelschema.RunResponse

type RunStream = largemodelschema.RunStream

type SessionTreePromotionCompletedData = largemodelschema.SessionTreePromotionCompletedData

type SessionTreePromotionFailedData = largemodelschema.SessionTreePromotionFailedData

type SessionTreePromotionStartedData = largemodelschema.SessionTreePromotionStartedData

type SessionTreeUpdatedData = largemodelschema.SessionTreeUpdatedData

type SkillActivateData = largemodelschema.SkillActivateData

type SkillDeactivateData = largemodelschema.SkillDeactivateData

type SkillDiscoverData = largemodelschema.SkillDiscoverData

type SkillResourceLoadData = largemodelschema.SkillResourceLoadData

type StopReason = largemodelschema.StopReason

type StreamProducer = largemodelschema.StreamProducer

type SubAgentEndData = largemodelschema.SubAgentEndData

type SubAgentStartData = largemodelschema.SubAgentStartData

type TextDeltaData = largemodelschema.TextDeltaData

type TodoItem = largemodelschema.TodoItem

type TodoUpdateData = largemodelschema.TodoUpdateData

type TokenBudgetExhaustedData = largemodelschema.TokenBudgetExhaustedData

type ToolCall = largemodelschema.ToolCall

type ToolCallEndData = largemodelschema.ToolCallEndData

type ToolCallStartData = largemodelschema.ToolCallStartData

type ToolDef = largemodelschema.ToolDef

type ToolExcludedData = largemodelschema.ToolExcludedData

type ToolResult = largemodelschema.ToolResult

type ToolResultData = largemodelschema.ToolResultData

type Usage = largemodelschema.Usage

type WorkspaceArtifactWrittenData = largemodelschema.WorkspaceArtifactWrittenData

type WorkspaceNoteWrittenData = largemodelschema.WorkspaceNoteWrittenData

type WorkspacePlanUpdatedData = largemodelschema.WorkspacePlanUpdatedData

type WorkspaceScratchWrittenData = largemodelschema.WorkspaceScratchWrittenData

const EventAgentEnd = largemodelschema.EventAgentEnd

const EventAgentStart = largemodelschema.EventAgentStart

const EventBudgetExceeded = largemodelschema.EventBudgetExceeded

const EventBudgetWarn = largemodelschema.EventBudgetWarn

const EventCheckpointWritten = largemodelschema.EventCheckpointWritten

const EventContextBuilt = largemodelschema.EventContextBuilt

const EventContextEdited = largemodelschema.EventContextEdited

const EventCustom = largemodelschema.EventCustom

const EventError = largemodelschema.EventError

const EventGuardCheck = largemodelschema.EventGuardCheck

const EventInterruptCreated = largemodelschema.EventInterruptCreated

const EventInterruptDecisionStored = largemodelschema.EventInterruptDecisionStored

const EventInterruptResumed = largemodelschema.EventInterruptResumed

const EventIterationStart = largemodelschema.EventIterationStart

const EventLLMCallEnd = largemodelschema.EventLLMCallEnd

const EventLLMCallError = largemodelschema.EventLLMCallError

const EventLLMCallStart = largemodelschema.EventLLMCallStart

const EventMCPCredentialDetected = largemodelschema.EventMCPCredentialDetected

const EventParamsResolved = largemodelschema.EventParamsResolved

const EventPendingInteraction = largemodelschema.EventPendingInteraction

const EventPhaseEnd = largemodelschema.EventPhaseEnd

const EventPhaseStart = largemodelschema.EventPhaseStart

const EventRouteSelected = largemodelschema.EventRouteSelected

const EventSessionTreePromotionCompleted = largemodelschema.EventSessionTreePromotionCompleted

const EventSessionTreePromotionFailed = largemodelschema.EventSessionTreePromotionFailed

const EventSessionTreePromotionStarted = largemodelschema.EventSessionTreePromotionStarted

const EventSessionTreeUpdated = largemodelschema.EventSessionTreeUpdated

const EventSkillActivate = largemodelschema.EventSkillActivate

const EventSkillDeactivate = largemodelschema.EventSkillDeactivate

const EventSkillDiscover = largemodelschema.EventSkillDiscover

const EventSkillResourceLoad = largemodelschema.EventSkillResourceLoad

const EventSubAgentEnd = largemodelschema.EventSubAgentEnd

const EventSubAgentStart = largemodelschema.EventSubAgentStart

const EventTextDelta = largemodelschema.EventTextDelta

const EventTodoUpdate = largemodelschema.EventTodoUpdate

const EventTokenBudgetExhausted = largemodelschema.EventTokenBudgetExhausted

const EventToolCallEnd = largemodelschema.EventToolCallEnd

const EventToolCallStart = largemodelschema.EventToolCallStart

const EventToolExcluded = largemodelschema.EventToolExcluded

const EventToolResult = largemodelschema.EventToolResult

const EventWorkspaceArtifactWritten = largemodelschema.EventWorkspaceArtifactWritten

const EventWorkspaceNoteWritten = largemodelschema.EventWorkspaceNoteWritten

const EventWorkspacePlanUpdated = largemodelschema.EventWorkspacePlanUpdated

const EventWorkspaceScratchWritten = largemodelschema.EventWorkspaceScratchWritten

const MessagePartFile = largemodelschema.MessagePartFile

const MessagePartImage = largemodelschema.MessagePartImage

const MessagePartText = largemodelschema.MessagePartText

const MessagePartThinking = largemodelschema.MessagePartThinking

const MessagePartToolCall = largemodelschema.MessagePartToolCall

const MessagePartToolResult = largemodelschema.MessagePartToolResult

const ProtocolAnthropicMessages = largemodelschema.ProtocolAnthropicMessages

const ProtocolOpenAIChat = largemodelschema.ProtocolOpenAIChat

const ProtocolOpenAIResponses = largemodelschema.ProtocolOpenAIResponses

const ResourceRead = largemodelschema.ResourceRead

const ResourceWrite = largemodelschema.ResourceWrite

const RoleAssistant = largemodelschema.RoleAssistant

const RoleSystem = largemodelschema.RoleSystem

const RoleTool = largemodelschema.RoleTool

const RoleUser = largemodelschema.RoleUser

const RouteReasonFailover = largemodelschema.RouteReasonFailover

const RouteReasonInitial = largemodelschema.RouteReasonInitial

const RouteReasonProbation = largemodelschema.RouteReasonProbation

const RouteReasonReuse = largemodelschema.RouteReasonReuse

const SessionTreeOpAdd = largemodelschema.SessionTreeOpAdd

const SessionTreeOpCreate = largemodelschema.SessionTreeOpCreate

const SessionTreeOpCursor = largemodelschema.SessionTreeOpCursor

const SessionTreeOpDelete = largemodelschema.SessionTreeOpDelete

const SessionTreeOpDeleteTree = largemodelschema.SessionTreeOpDeleteTree

const SessionTreeOpUpdate = largemodelschema.SessionTreeOpUpdate

const StopReasonBudgetExhausted = largemodelschema.StopReasonBudgetExhausted

const StopReasonComplete = largemodelschema.StopReasonComplete

const StopReasonInterrupted = largemodelschema.StopReasonInterrupted

const StopReasonMaxIterations = largemodelschema.StopReasonMaxIterations

const ToolModeAll = largemodelschema.ToolModeAll

const ToolModeAllow = largemodelschema.ToolModeAllow

const ToolModeNone = largemodelschema.ToolModeNone

const ToolSourceAgent = largemodelschema.ToolSourceAgent

const ToolSourceLocal = largemodelschema.ToolSourceLocal

const ToolSourceMCP = largemodelschema.ToolSourceMCP

var ErrProtocolMismatch = largemodelschema.ErrProtocolMismatch

var ErrRunStreamClosed = largemodelschema.ErrRunStreamClosed

var ErrUnknownProtocol = largemodelschema.ErrUnknownProtocol

var DispatchEvent = largemodelschema.DispatchEvent

var EmitCustomData = largemodelschema.EmitCustomData

var EmitterFromContext = largemodelschema.EmitterFromContext

var ErrorResult = largemodelschema.ErrorResult

var EventDispatcherFromContext = largemodelschema.EventDispatcherFromContext

var FileFromBytes = largemodelschema.FileFromBytes

var FileFromID = largemodelschema.FileFromID

var GetRunValue = largemodelschema.GetRunValue

var ImageFromBytes = largemodelschema.ImageFromBytes

var ImageFromURL = largemodelschema.ImageFromURL

var MergeStreams = largemodelschema.MergeStreams

var NewAssistantTurn = largemodelschema.NewAssistantTurn

var NewEvent = largemodelschema.NewEvent

var NewMessage = largemodelschema.NewMessage

var NewMessageWithOrigin = largemodelschema.NewMessageWithOrigin

var NewRunStream = largemodelschema.NewRunStream

var NewSystemMessage = largemodelschema.NewSystemMessage

var NewTextMessage = largemodelschema.NewTextMessage

var NewToolResultMessage = largemodelschema.NewToolResultMessage

var NewUserMessage = largemodelschema.NewUserMessage

var NewUserMessageWithParts = largemodelschema.NewUserMessageWithParts

var ProtocolOf = largemodelschema.ProtocolOf

var SessionIDFromContext = largemodelschema.SessionIDFromContext

var SetRunValue = largemodelschema.SetRunValue

var TextResult = largemodelschema.TextResult

var WithEmitter = largemodelschema.WithEmitter

var WithEventDispatcher = largemodelschema.WithEventDispatcher

var WithRunValues = largemodelschema.WithRunValues

var WithSessionID = largemodelschema.WithSessionID
