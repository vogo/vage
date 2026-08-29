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

// Package embedcore is the shared contract point between the vector root
// package and the embedding providers under vector/provider.
//
// It exists purely to break an import cycle: the root package's
// config-driven constructor imports every provider, so providers must not
// import the root package back. Sentinel errors that both sides must agree
// on therefore live here, with the root package re-exporting the very same
// instances under their original names — errors.Is against either spelling
// keeps matching.
//
// Nothing here is part of the public API. Callers compare against the root
// package's exported names (e.g. vector.ErrEmptyQuery).
package embedcore

import "errors"

// ErrEmptyQuery is the single underlying instance behind
// vector.ErrEmptyQuery. Providers return it for empty input so callers can
// route on the standard sentinel without importing this internal package.
//
// The message is deliberately the root package's wording, not an
// embedcore-specific one: it is user-visible through vector.ErrEmptyQuery
// and existing log assertions pin it.
var ErrEmptyQuery = errors.New("vector: empty query")
