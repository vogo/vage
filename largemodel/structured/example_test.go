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

package structured_test

import (
	"context"
	"fmt"

	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/largemodel/structured"
	"github.com/vogo/vage/schema"
)

type score struct {
	Value int `json:"value"`
}

// ExampleCall decodes the model's final text into T. Native structured output
// is required by default; AllowPromptFallback must be opted into separately.
// WithRepairAttempts is content repair, not transport retry.
func ExampleCall() {
	fake := &largemodel.FakeCaller{
		Responses: []*largemodel.Response{
			largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, `{"value": 3}`, schema.Usage{}),
		},
		DeclaredSet: true,
		Declared:    largemodel.Capabilities{StructuredOutput: largemodel.SupportNative},
	}

	got, err := structured.Call[score](context.Background(), fake, &largemodel.Request{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "n")},
	}, structured.RequireNative(), structured.WithValidation())
	if err != nil {
		fmt.Println("err", err)
		return
	}

	fmt.Println(got.Value.Value)
	fmt.Println(got.Response != nil)
	// Output:
	// 3
	// true
}
