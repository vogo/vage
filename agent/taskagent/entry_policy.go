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
	"context"

	"github.com/vogo/vage/schema"
)

// entryPolicy declares which cross-cut layers a run-class entry executes.
// Each public entry references one of the package constants at the top of
// its body so reviewers can audit the matrix without reading scattered
// conditionals. See doc/domains/agent/agent-core/agent-core.md.
type entryPolicy struct {
	initRunValues   bool
	inputGuards     bool
	agentMiddleware bool
	paramResolver   bool
}

// policyFreshRun is the entry policy for Run and RunStream.
var policyFreshRun = entryPolicy{
	initRunValues:   true,
	inputGuards:     true,
	agentMiddleware: true,
	paramResolver:   true,
}

// policyResume is the entry policy for Resume and ResumeInterrupt.
var policyResume = entryPolicy{
	initRunValues:   true,
	inputGuards:     false,
	agentMiddleware: false,
	paramResolver:   false,
}

// bindRunValues attaches a fresh run-value store when policy requires it.
func bindRunValues(ctx context.Context, policy entryPolicy) context.Context {
	if !policy.initRunValues {
		return ctx
	}

	return schema.WithRunValues(ctx)
}
