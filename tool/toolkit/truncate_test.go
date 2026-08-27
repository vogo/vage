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

package toolkit

import (
	"testing"

	"github.com/vogo/vage/tool"
)

// TestTruncateUTF8_MatchesFrameworkEntryPoint pins the deprecated alias to the
// framework entry point, so callers can migrate to tool.TruncateUTF8 without
// re-verifying behaviour.
func TestTruncateUTF8_MatchesFrameworkEntryPoint(t *testing.T) {
	inputs := []string{"", "hello", "世界", "ok🚀", "id=世界", "a\nb\tc"}
	limits := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 16, 64}

	for _, s := range inputs {
		for _, limit := range limits {
			got := TruncateUTF8(s, limit)
			want := tool.TruncateUTF8(s, limit)
			if got != want {
				t.Errorf("toolkit.TruncateUTF8(%q, %d) = %q, want %q", s, limit, got, want)
			}
		}
	}
}
