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

import "testing"

// TestRequest_Clone_PreservesResponseSchema pins that Clone treats
// ResponseSchema like every other read-only request field: it survives the
// copy, by value, without vage attempting to deep-copy the schema object
// underneath it.
func TestRequest_Clone_PreservesResponseSchema(t *testing.T) {
	respSchema := map[string]any{"type": "object"}
	req := &Request{Model: "gpt-4", ResponseSchema: respSchema}

	clone := req.Clone()

	if clone.ResponseSchema == nil {
		t.Fatal("Clone dropped ResponseSchema")
	}

	got, ok := clone.ResponseSchema.(map[string]any)
	if !ok || got["type"] != "object" {
		t.Fatalf("Clone.ResponseSchema = %#v, want %#v", clone.ResponseSchema, respSchema)
	}
}
