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

// Package openai is the compatibility shim for the OpenAI embedder's
// former home. The implementation moved to
// github.com/vogo/vage/vector/provider/openais when embedding gained a
// provider layer alongside Voyage.
//
// Everything here is an alias or a one-line forward to that package, not
// a second implementation: openai.Embedder and openais.Embedder are the
// same type, so values cross between the two import paths freely and
// type assertions and errors.Is keep their existing results. Existing
// code recompiles untouched.
//
// Deprecated: import github.com/vogo/vage/vector/provider/openais
// instead. No removal date is set for this path.
package openai

import (
	"net/http"

	"github.com/vogo/vage/vector/provider/openais"
)

// Endpoint and model defaults. These are the same values as their
// openais counterparts.
//
// Deprecated: use the openais constants of the same names.
const (
	DefaultBaseURL               = openais.DefaultBaseURL
	DefaultModel                 = openais.DefaultModel
	MaxInputTokensTextEmbedding3 = openais.MaxInputTokensTextEmbedding3
)

// ErrMissingAPIKey is the same error instance as openais.ErrMissingAPIKey,
// so errors.Is matches regardless of which name a caller compares against.
//
// Deprecated: use openais.ErrMissingAPIKey.
var ErrMissingAPIKey = openais.ErrMissingAPIKey

// Embedder is an alias for openais.Embedder — the same type, not a
// wrapper.
//
// Deprecated: use openais.Embedder.
type Embedder = openais.Embedder

// Option is an alias for openais.Option, so options from either package
// can be passed to either constructor.
//
// Deprecated: use openais.Option.
type Option = openais.Option

// New forwards to openais.New.
//
// Deprecated: use openais.New.
func New(opts ...Option) (*Embedder, error) { return openais.New(opts...) }

// WithAPIKey forwards to openais.WithAPIKey.
//
// Deprecated: use openais.WithAPIKey.
func WithAPIKey(k string) Option { return openais.WithAPIKey(k) }

// WithBaseURL forwards to openais.WithBaseURL.
//
// Deprecated: use openais.WithBaseURL.
func WithBaseURL(u string) Option { return openais.WithBaseURL(u) }

// WithModel forwards to openais.WithModel.
//
// Deprecated: use openais.WithModel.
func WithModel(m string) Option { return openais.WithModel(m) }

// WithDimensions forwards to openais.WithDimensions.
//
// Deprecated: use openais.WithDimensions.
func WithDimensions(d int) Option { return openais.WithDimensions(d) }

// WithHTTPClient forwards to openais.WithHTTPClient.
//
// Deprecated: use openais.WithHTTPClient.
func WithHTTPClient(c *http.Client) Option { return openais.WithHTTPClient(c) }

// WithMaxInputTokens forwards to openais.WithMaxInputTokens.
//
// Deprecated: use openais.WithMaxInputTokens.
func WithMaxInputTokens(n int) Option { return openais.WithMaxInputTokens(n) }
