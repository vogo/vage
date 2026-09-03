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
// Infer keeps the original panic-on-illegal-type contract. New static
// declarations should prefer MustInfer; dynamic registration should use
// Compile (see ExampleCompile).
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

// ExampleMustInfer is the explicit panic constructor for static declarations
// where an illegal parameter type is a programming mistake. It shares
// Compile's construction path and panics with the same payload as Infer.
func ExampleMustInfer() {
	type echoArgs struct {
		Text string `json:"text"`
	}

	def, handler := tool.MustInfer("echo", "echo the text",
		func(_ context.Context, a echoArgs) (schema.ToolResult, error) {
			return schema.TextResult("", a.Text), nil
		})

	reg := tool.NewRegistry()
	_ = reg.Register(def, handler)

	res, err := reg.Execute(context.Background(), "echo", `{"text":"hi"}`)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Content[0].Text)

	// Output:
	// hi
}

// ExampleCompile shows recoverable construction for batch registration: an
// illegal definition returns an error, the caller skips it, and later tools
// still register. Prefer Compile whenever a single bad type must not abort
// the rest of the catalog. Successful products are the same ToolDef+Handler
// pair Register already accepts.
func ExampleCompile() {
	type grepArgs struct {
		Pattern string `json:"pattern" jsonschema_description:"Regular expression to search for"`
		Path    string `json:"path,omitempty"`
	}

	reg := tool.NewRegistry()

	_, _, err := tool.Compile("bad", "illegal argument type",
		func(context.Context, int) (schema.ToolResult, error) {
			return schema.ToolResult{}, nil
		})
	if err != nil {
		fmt.Println("skipped bad tool")
	}

	def, handler, err := tool.Compile("grep", "Search a file for a pattern",
		func(_ context.Context, a grepArgs) (schema.ToolResult, error) {
			return schema.TextResult("", fmt.Sprintf("search %q under %q", a.Pattern, a.Path)), nil
		})
	if err != nil {
		panic(err)
	}
	_ = reg.Register(def, handler)

	res, err := reg.Execute(context.Background(), "grep", `{"pattern":"foo"}`)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Content[0].Text)

	// Output:
	// skipped bad tool
	// search "foo" under ""
}
