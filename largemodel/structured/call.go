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

// Package structured types a non-streaming model call onto a Go value T.
package structured

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
)

// Result is a successful structured call: the decoded value and the raw
// model response that produced it.
type Result[T any] struct {
	Value    T
	Response *largemodel.Response
}

type callOptions struct {
	promptFallback bool
	validate       bool
	repair         int
	validator      func(any) error
}

// Option configures Call.
type Option func(*callOptions)

// RequireNative requires native structured output. It is the default.
func RequireNative() Option {
	return func(o *callOptions) { o.promptFallback = false }
}

// AllowPromptFallback opts into prompt-constrained structured output when
// native mapping is unavailable. It is never implied.
func AllowPromptFallback() Option {
	return func(o *callOptions) { o.promptFallback = true }
}

// WithValidation validates the JSON value against the effective schema after
// it unmarshals into T. Keywords this library does not enforce are not treated
// as validated.
func WithValidation() Option {
	return func(o *callOptions) { o.validate = true }
}

// WithRepairAttempts allows at most n content-repair calls after the first
// response. n=0 disables repair. A negative n fails before any model call.
// Transport and provider errors do not consume a repair attempt.
func WithRepairAttempts(n int) Option {
	return func(o *callOptions) { o.repair = n }
}

// WithValidator runs after JSON decoding (and schema validation, when
// requested) for constraints the schema cannot express.
func WithValidator[T any](fn func(T) error) Option {
	return func(o *callOptions) {
		if fn == nil {
			return
		}

		o.validator = func(v any) error {
			typed, ok := v.(T)
			if !ok {
				return fmt.Errorf("vage: structured validator expected %T", *new(T))
			}

			return fn(typed)
		}
	}
}

// Call performs one non-streaming structured call. req is never modified.
func Call[T any](ctx context.Context, caller largemodel.Caller, req *largemodel.Request, opts ...Option) (Result[T], error) {
	var zero Result[T]
	if caller == nil {
		return zero, &Error{Stage: StageConfig, Err: fmt.Errorf("vage: structured call requires a caller")}
	}

	cfg := callOptions{}
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.repair < 0 {
		return zero, &Error{Stage: StageConfig, Err: fmt.Errorf("vage: repair attempts must not be negative")}
	}

	base, resolved, err := prepareRequest[T](req)
	if err != nil {
		return zero, &Error{Stage: StageConfig, Err: err}
	}

	bound := largemodel.RequireNativeCapabilities(caller)
	if cfg.promptFallback {
		bound = largemodel.AllowPromptFallback(bound)
	}

	var lastResp *largemodel.Response
	var lastRaw string
	work := base
	attempts := 1 + cfg.repair
	proto := caller.Protocol()

	for i := range attempts {
		if err := ctx.Err(); err != nil {
			return zero, &Error{Stage: StageTransport, Err: err, Response: lastResp, RawText: lastRaw}
		}

		resp, callErr := bound.Call(ctx, work)
		if callErr != nil {
			stage := StageTransport
			if _, ok := errors.AsType[*largemodel.CapabilityError](callErr); ok {
				stage = StageCapability
			}

			return zero, &Error{Stage: stage, Err: callErr, Response: lastResp, RawText: lastRaw}
		}

		lastResp = resp
		lastRaw = resp.Message.Text()

		value, stage, decodeErr := decodeValue[T](lastRaw, resolved, cfg)
		if decodeErr == nil {
			return Result[T]{Value: value, Response: resp}, nil
		}

		if i == attempts-1 {
			return zero, &Error{Stage: stage, Err: decodeErr, Response: resp, RawText: lastRaw}
		}

		work = base.Clone()
		work.Messages = appendRepair(proto, base.Messages, lastRaw, decodeErr.Error())
	}

	return zero, &Error{Stage: StageDecode, Err: fmt.Errorf("vage: structured call exhausted"), Response: lastResp, RawText: lastRaw}
}

func prepareRequest[T any](req *largemodel.Request) (*largemodel.Request, *jsonschema.Resolved, error) {
	if req == nil {
		req = &largemodel.Request{}
	}

	work := req.Clone()
	if work.ResponseSchema == nil {
		inferred, err := jsonschema.For[T](nil)
		if err != nil {
			return nil, nil, fmt.Errorf("vage: derive response schema from %T: %w", *new(T), err)
		}

		asAny, err := schemaAsAny(inferred)
		if err != nil {
			return nil, nil, err
		}

		work.ResponseSchema = asAny
	}

	resolved, err := resolveSchema(work.ResponseSchema)
	if err != nil {
		return nil, nil, err
	}

	return work, resolved, nil
}

func schemaAsAny(s *jsonschema.Schema) (any, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("vage: encode derived response schema: %w", err)
	}

	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("vage: decode derived response schema: %w", err)
	}

	return out, nil
}

func resolveSchema(raw any) (*jsonschema.Resolved, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("vage: encode response schema: %w", err)
	}

	var s jsonschema.Schema
	if err := json.Unmarshal(encoded, &s); err != nil {
		return nil, fmt.Errorf("vage: parse response schema: %w", err)
	}

	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("vage: resolve response schema: %w", err)
	}

	return resolved, nil
}

func decodeValue[T any](raw string, resolved *jsonschema.Resolved, cfg callOptions) (T, Stage, error) {
	var value T
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, StageDecode, err
	}

	if cfg.validate {
		var instance any
		if err := json.Unmarshal([]byte(raw), &instance); err != nil {
			return value, StageDecode, err
		}

		if err := resolved.Validate(instance); err != nil {
			return value, StageSchema, err
		}
	}

	if cfg.validator != nil {
		if err := cfg.validator(value); err != nil {
			return value, StageValidate, err
		}
	}

	return value, "", nil
}

func appendRepair(proto schema.Protocol, msgs []schema.Message, raw, reason string) []schema.Message {
	out := make([]schema.Message, len(msgs), len(msgs)+1)
	copy(out, msgs)
	out = append(out, schema.NewUserMessage(proto, fmt.Sprintf(
		"The previous JSON did not satisfy the required schema.\nReason: %s\nPrevious output:\n%s\nRespond with a single JSON value that matches the schema. Output raw JSON only.",
		reason, raw,
	)))

	return out
}
