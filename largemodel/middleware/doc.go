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

// Package middleware provides cross-cutting Caller decorators: caching,
// rate limiting, timeouts, budgets, logging, metrics, and debug capture.
//
// The Middleware interface and chain assembly live in the root largemodel
// package; implementations here wrap a largemodel.Caller and see only the
// protocol-neutral Request/Response envelopes. Context editing lives in the
// sibling contexteditor subpackage.
package middleware
