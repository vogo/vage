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

package tool

import (
	"context"

	"github.com/vogo/vagent/schema"
)

// ToolHandler is a function that handles a tool invocation.
// name is the tool name, args is the raw JSON arguments string.
type ToolHandler func(ctx context.Context, name, args string) (schema.ToolResult, error)

// ToolExecutor executes a named tool with the given arguments.
type ToolExecutor interface {
	Execute(ctx context.Context, name, args string) (schema.ToolResult, error)
}

// ToolRegistry manages tool definitions and their handlers.
type ToolRegistry interface {
	ToolExecutor
	Register(def schema.ToolDef, handler ToolHandler) error
	Unregister(name string) error
	Get(name string) (schema.ToolDef, bool)
	List() []schema.ToolDef
	Merge(defs []schema.ToolDef) // merge external tool definitions (for MCP)
}
