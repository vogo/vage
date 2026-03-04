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

package agent

import (
	"context"
	"errors"

	"github.com/vogo/vagent/schema"
)

// CustomAgent delegates its Run to a user-provided RunFunc.
type CustomAgent struct {
	agentMeta
	runFunc RunFunc
}

var _ Agent = (*CustomAgent)(nil)

// NewCustomAgent creates a CustomAgent with the given RunFunc.
func NewCustomAgent(cfg Config, fn RunFunc) *CustomAgent {
	return &CustomAgent{
		agentMeta: newAgentMeta(cfg),
		runFunc:   fn,
	}
}

// Run delegates to the configured RunFunc. Returns an error if RunFunc is nil.
func (a *CustomAgent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	if a.runFunc == nil {
		return nil, errors.New("vagent: CustomAgent has no RunFunc configured")
	}
	return a.runFunc(ctx, req)
}
