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

package schema

import (
	"encoding/json"
	"testing"
)

func TestResourceMode_ConstantValues(t *testing.T) {
	if ResourceRead != "read" {
		t.Errorf("ResourceRead = %q, want %q", ResourceRead, "read")
	}
	if ResourceWrite != "write" {
		t.Errorf("ResourceWrite = %q, want %q", ResourceWrite, "write")
	}
}

func TestResourceRef_JSONRoundTrip(t *testing.T) {
	in := ResourceRef{ID: "/abs/path/to/file.go", Mode: ResourceWrite}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	const want = `{"id":"/abs/path/to/file.go","mode":"write"}`
	if string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var out ResourceRef
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip = %+v, want %+v", out, in)
	}
}
