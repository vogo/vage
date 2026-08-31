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

package largemodel

import (
	"time"

	"github.com/vogo/vage/largemodel/router"
)

// defaultComposeConcurrency is how many pools a compose caller builds at most.
// A provider router pool serves one call at a time, so this is also the number
// of model calls the caller can have in flight before one has to wait.
const defaultComposeConcurrency = 8

// ComposeOption configures a compose caller.
type ComposeOption func(*composeConfig)

type composeConfig struct {
	routerOpts  []router.Option
	concurrency int

	// Provider client options reach the single-endpoint constructors, which
	// own the client they build. The declaratively built pools take their
	// connection details from their endpoint specs instead, and ignore these.
	// Each provider's options are held in a struct that provider's compose
	// glue owns, so no vendor type reaches this neutral file.
	openAI    openAIComposeOptions
	anthropic anthropicComposeOptions
}

// WithRetryPolicy sets the in-call retry policy for each router pool the
// caller builds: base is the wait before the first retry and each further retry
// doubles it.
func WithRetryPolicy(base time.Duration, maxRetries int) ComposeOption {
	return WithComposeRouterOptions(router.WithRetryPolicy(base, maxRetries))
}

// WithRecoverTime sets how long a dead endpoint stays out of rotation before it
// returns on probation.
func WithRecoverTime(d time.Duration) ComposeOption {
	return WithComposeRouterOptions(router.WithRecoverTime(d))
}

// WithAttemptObserver registers a callback invoked when each endpoint attempt
// finishes. Observers may run concurrently when several pools are active.
func WithAttemptObserver(fn func(router.AttemptResult)) ComposeOption {
	return WithComposeRouterOptions(router.WithAttemptObserver(fn))
}

// WithConcurrency caps how many router pools the caller builds. WithComposeConcurrency
// is a deprecated alias.
func WithConcurrency(n int) ComposeOption {
	return WithComposeConcurrency(n)
}

// WithComposeRouterOptions passes low-level router options through to every pool
// the caller builds. Prefer [WithRetryPolicy], [WithRecoverTime] and
// [WithAttemptObserver] for the common cases.
//
// A caller may dispatch through several pools concurrently. Consequently, an
// attempt observer registered with [router.WithAttemptObserver] may be called
// concurrently and must synchronize access to any shared state itself.
func WithComposeRouterOptions(opts ...router.Option) ComposeOption {
	return func(c *composeConfig) {
		c.routerOpts = append(c.routerOpts, opts...)
	}
}

// WithComposeConcurrency caps how many pools the caller builds, and thus how
// many model calls it serves concurrently. A call arriving when every pool is
// busy waits for one to free up rather than failing. Zero or negative selects
// the default.
//
// Pools are built lazily, so a caller that is never used concurrently only
// ever holds one.
func WithComposeConcurrency(n int) ComposeOption {
	return func(c *composeConfig) {
		c.concurrency = n
	}
}

func newComposeConfig(opts ...ComposeOption) *composeConfig {
	cfg := &composeConfig{concurrency: defaultComposeConcurrency}

	for _, o := range opts {
		o(cfg)
	}

	if cfg.concurrency <= 0 {
		cfg.concurrency = defaultComposeConcurrency
	}

	return cfg
}
