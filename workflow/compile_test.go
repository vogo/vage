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

package workflow

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSetRejectsMismatchedValueTypeAtCompileTime(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", os.DevNull, "./testdata/wrongset")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected testdata/wrongset to fail type checking")
	}
	msg := string(out)
	if !strings.Contains(msg, "cannot use") && !strings.Contains(msg, "mismatched types") && !strings.Contains(msg, "string") {
		t.Fatalf("expected a type error, got:\n%s", msg)
	}
}
