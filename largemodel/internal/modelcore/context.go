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

package modelcore

import "context"

type ctxKey int

const (
	ctxPromptFallback ctxKey = iota
	ctxEligibleAliases
)

// WithPromptFallback marks the call as having opted into structured-output
// prompt fallback. Codecs without a native mapping may then degrade the
// schema into a system instruction.
func WithPromptFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxPromptFallback, true)
}

// PromptFallback reports whether WithPromptFallback was set on ctx.
func PromptFallback(ctx context.Context) bool {
	allowed, _ := ctx.Value(ctxPromptFallback).(bool)

	return allowed
}

// WithEligibleAliases restricts a compose dispatch to these endpoint aliases.
// An empty slice is a restriction to nobody; a missing value is no restriction.
func WithEligibleAliases(ctx context.Context, aliases []string) context.Context {
	return context.WithValue(ctx, ctxEligibleAliases, append([]string(nil), aliases...))
}

// EligibleAliases returns the per-call alias restriction, or nil when the
// caller did not restrict routing.
func EligibleAliases(ctx context.Context) []string {
	aliases, ok := ctx.Value(ctxEligibleAliases).([]string)
	if !ok {
		return nil
	}

	return aliases
}
