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

package tool_test

import (
	"context"
	"fmt"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// ExampleInfer shows the struct -> Infer -> Register flow, with the inferred
// tool coexisting alongside a hand-written one in the same registry.
func ExampleInfer() {
	type grepArgs struct {
		Pattern string `json:"pattern" jsonschema_description:"Regular expression to search for"`
		Path    string `json:"path,omitempty"`
	}

	def, handler := tool.Infer("grep", "Search a file for a pattern",
		func(ctx context.Context, a grepArgs) (schema.ToolResult, error) {
			return schema.TextResult("", fmt.Sprintf("search %q under %q", a.Pattern, a.Path)), nil
		})

	manualDef := schema.ToolDef{
		Name:        "ping",
		Description: "respond with pong",
		Parameters:  map[string]any{"type": "object"},
	}
	manualHandler := func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.TextResult("", "pong"), nil
	}

	reg := tool.NewRegistry()
	_ = reg.Register(def, handler)
	_ = reg.Register(manualDef, manualHandler)

	res, err := reg.Execute(context.Background(), "grep", `{"pattern":"foo"}`)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Content[0].Text)

	res, err = reg.Execute(context.Background(), "ping", `{}`)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Content[0].Text)

	// Output:
	// search "foo" under ""
	// pong
}
