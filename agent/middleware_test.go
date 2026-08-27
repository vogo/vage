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

package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vogo/vage/schema"
)

// recorder builds a Middleware that appends "<name>:pre" before calling next
// and "<name>:post" after it returns, so a test can read the whole traversal
// order off one slice.
func recorder(trace *[]string, name string) Middleware {
	return MiddlewareFunc(func(next RunFunc) RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			*trace = append(*trace, name+":pre")
			resp, err := next(ctx, req)
			*trace = append(*trace, name+":post")

			return resp, err
		}
	})
}

func baseFunc(trace *[]string) RunFunc {
	return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		*trace = append(*trace, "base")

		return &schema.RunResponse{}, nil
	}
}

func TestChainMiddleware_FirstIsOutermost(t *testing.T) {
	var trace []string

	chained := ChainMiddleware(baseFunc(&trace), recorder(&trace, "a"), recorder(&trace, "b"), recorder(&trace, "c"))
	if _, err := chained(context.Background(), &schema.RunRequest{}); err != nil {
		t.Fatalf("chained: %v", err)
	}

	want := []string{"a:pre", "b:pre", "c:pre", "base", "c:post", "b:post", "a:post"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestChainMiddleware_SkipsNil(t *testing.T) {
	var trace []string

	chained := ChainMiddleware(baseFunc(&trace), nil, recorder(&trace, "a"), nil)
	if _, err := chained(context.Background(), &schema.RunRequest{}); err != nil {
		t.Fatalf("chained: %v", err)
	}

	want := []string{"a:pre", "base", "a:post"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestChainMiddleware_NoMiddlewareReturnsBase(t *testing.T) {
	var trace []string

	if _, err := ChainMiddleware(baseFunc(&trace))(context.Background(), &schema.RunRequest{}); err != nil {
		t.Fatalf("chained: %v", err)
	}

	if !slices.Equal(trace, []string{"base"}) {
		t.Errorf("trace = %v, want [base]", trace)
	}
}

func TestChainMiddleware_ShortCircuitSkipsBase(t *testing.T) {
	var trace []string

	stub := MiddlewareFunc(func(_ RunFunc) RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			trace = append(trace, "stub")

			return &schema.RunResponse{StopReason: schema.StopReasonComplete}, nil
		}
	})

	resp, err := ChainMiddleware(baseFunc(&trace), stub, recorder(&trace, "unreached"))(context.Background(), &schema.RunRequest{})
	if err != nil {
		t.Fatalf("chained: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}
	if !slices.Equal(trace, []string{"stub"}) {
		t.Errorf("trace = %v, want [stub]: downstream must not run", trace)
	}
}

func TestChainMiddleware_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("denied")

	blocker := MiddlewareFunc(func(_ RunFunc) RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, sentinel
		}
	})

	var trace []string

	_, err := ChainMiddleware(baseFunc(&trace), recorder(&trace, "a"), blocker)(context.Background(), &schema.RunRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	// The outer middleware still gets its post phase; only base is skipped.
	if !slices.Equal(trace, []string{"a:pre", "a:post"}) {
		t.Errorf("trace = %v, want [a:pre a:post]", trace)
	}
}
