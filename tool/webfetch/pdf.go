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

package webfetch

import (
	"strings"
	"unicode"
)

func extractPDFText(body []byte) string {
	var out []string
	var current strings.Builder
	inString := false
	escape := false

	flush := func() {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if looksLikeReadablePDFText(text) {
			out = append(out, text)
		}
	}

	for _, b := range body {
		if !inString {
			if b == '(' {
				inString = true
				escape = false
				current.Reset()
			}
			continue
		}

		if escape {
			switch b {
			case 'n':
				current.WriteByte('\n')
			case 'r':
				current.WriteByte('\n')
			case 't':
				current.WriteByte('\t')
			default:
				current.WriteByte(b)
			}
			escape = false
			continue
		}

		switch b {
		case '\\':
			escape = true
		case ')':
			inString = false
			flush()
		default:
			current.WriteByte(b)
		}
	}

	text := collapseWhitespace(strings.Join(out, "\n"))
	return strings.TrimSpace(text)
}

func looksLikeReadablePDFText(text string) bool {
	if text == "" {
		return false
	}
	printable := 0
	letters := 0
	for _, r := range text {
		if unicode.IsPrint(r) {
			printable++
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}
	return printable >= len([]rune(text))/2 && letters >= 3
}
