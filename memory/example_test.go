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

package memory_test

import (
	"context"
	"fmt"

	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

func ExampleSummarizeWhenOverBudget() {
	summarizer := func(_ context.Context, older []schema.Message) (string, error) {
		return fmt.Sprintf("summarized %d earlier messages", len(older)), nil
	}

	c := memory.SummarizeWhenOverBudget(
		summarizer,
		memory.KeepRecentTurns(1),
		memory.TargetUtilization(0.8),
	)

	var history []schema.Message
	for i := range 3 {
		history = append(
			history,
			schema.NewUserMessage(schema.ProtocolOpenAIChat, fmt.Sprintf("user-%d %s", i, "xxxxxxxxxxxxxxxxxxxx")),
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, fmt.Sprintf("asst-%d %s", i, "yyyyyyyyyyyyyyyyyyyy")),
		)
	}

	out, err := c.CompressWithBudget(context.Background(), memory.CompressionInput{
		Messages: history,
		Budget: memory.Budget{
			ModelContextTokens: 80,
			AvailableHistory:   30,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(out[0].Text())
	fmt.Println(out[len(out)-2].Role(), out[len(out)-1].Role())
	// Output:
	// summarized 4 earlier messages
	// user assistant
}
