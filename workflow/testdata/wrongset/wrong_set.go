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

package wrongset

import "github.com/vogo/vage/workflow"

type ticket struct {
	N int
}

var n = workflow.NewField("n",
	func(s ticket) int { return s.N },
	func(s *ticket, v int) { s.N = v },
)

// A string is not assignable to Field[ticket, int]; this file exists so
// TestSetRejectsMismatchedValueTypeAtCompileTime can prove the compiler
// rejects it. No runtime any-assertion is involved.
var _ = workflow.Set(n, "nope")
