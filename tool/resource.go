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

import "github.com/vogo/vage/schema"

// ResourceTracker is implemented by tools that read or write identifiable
// resources (files, db rows, network endpoints, etc.). The canonical
// contract lives in schema; tool re-exports it so existing call sites
// keep using tool.ResourceTracker without migration.
//
// Note: ResourceTracker is unrelated to toolkit.ReadTracker. The latter
// records "which paths this agent has already read" for read-before-edit
// safety. ResourceTracker exposes "which resources this single invocation
// touches and how" for context editing.
type ResourceTracker = schema.ResourceTracker

// ResourceRef identifies one resource touched by a tool invocation,
// together with the access mode (read vs write).
type ResourceRef = schema.ResourceRef

// ResourceMode is the access mode reported by ResourceRef.
type ResourceMode = schema.ResourceMode

// Resource access modes recognised by ContextEditorMiddleware.
const (
	ResourceRead  = schema.ResourceRead
	ResourceWrite = schema.ResourceWrite
)
