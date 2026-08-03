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

// Package testenv reads the credentials the integration tests need from the
// environment.
//
// It lives under integrations/internal because reading the environment is a
// test-harness concern: vage's own packages take credentials and base URLs
// from the caller and never consult the environment themselves.
package testenv

import "os"

// First returns the value of the first environment variable that is set and
// non-empty, or the empty string when none is. It lets a test accept both
// vage's own variable names and the vendor's.
func First(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}

	return ""
}
