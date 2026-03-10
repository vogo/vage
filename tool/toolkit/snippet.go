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
	"fmt"
	"strings"
)

const defaultSnippetContext = 3

// GenerateEditSnippet returns a numbered snippet of content around byteOffset,
// showing contextLines lines before and after. It helps agents verify that an
// edit was applied to the intended location.
func GenerateEditSnippet(content string, byteOffset, contextLines int) string {
	if len(content) == 0 {
		return ""
	}

	if contextLines <= 0 {
		contextLines = defaultSnippetContext
	}

	if byteOffset < 0 {
		byteOffset = 0
	}

	if byteOffset >= len(content) {
		byteOffset = len(content) - 1
	}

	lines := strings.Split(content, "\n")

	// Find which line contains byteOffset.
	pos := 0
	targetLine := 0

	for i, line := range lines {
		nextPos := pos + len(line) + 1 // +1 for \n

		if byteOffset < nextPos {
			targetLine = i

			break
		}

		pos = nextPos

		if i == len(lines)-1 {
			targetLine = i
		}
	}

	start := max(0, targetLine-contextLines)
	end := min(len(lines), targetLine+contextLines+1)

	var buf strings.Builder

	for i := start; i < end; i++ {
		fmt.Fprintf(&buf, "%4d| %s\n", i+1, lines[i])
	}

	return buf.String()
}
