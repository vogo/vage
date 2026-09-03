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

package schema_test

import (
	"fmt"

	"github.com/vogo/vage/schema"
)

// ExampleImageFromURL builds a canonical image part. Empty URLs and illegal
// MIME types fail here rather than later in a codec.
func ExampleImageFromURL() {
	part, err := schema.ImageFromURL("https://example.com/cat.png")
	if err != nil {
		fmt.Println("err")
		return
	}

	msg := schema.NewUserMessageWithParts(schema.ProtocolOpenAIChat, []schema.MessagePart{
		{Type: schema.MessagePartText, Text: "what is this?"},
		part,
	})
	fmt.Println(part.Type)
	fmt.Println(msg.Validate() == nil)
	_, err = schema.ImageFromURL("")
	fmt.Println(err != nil)
	// Output:
	// image
	// true
	// true
}

// ExampleFileFromBytes copies inline bytes and allows an empty filename so
// Anthropic callers can omit it. OpenAI still rejects unnamed inline files
// in the codec, before the backend.
func ExampleFileFromBytes() {
	part, err := schema.FileFromBytes([]byte("%PDF"), "application/pdf", "report.pdf")
	if err != nil {
		fmt.Println("err")
		return
	}

	fmt.Println(part.Type)
	fmt.Println(part.Filename)
	_, err = schema.FileFromBytes(nil, "application/pdf", "report.pdf")
	fmt.Println(err != nil)
	// Output:
	// file
	// report.pdf
	// true
}
