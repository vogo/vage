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

package workflow

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/schema"
)

type stubRunner struct {
	fn func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
}

func (s stubRunner) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return s.fn(ctx, req)
}

func TestAdaptRunnerMapsRequestAndPatch(t *testing.T) {
	query := NewField(
		"query",
		func(s counter) int { return s.A },
		func(s *counter, v int) { s.A = v },
	)
	out := NewField(
		"out",
		func(s counter) int { return s.B },
		func(s *counter, v int) { s.B = v },
	)

	var sawSession string
	runner := stubRunner{fn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		sawSession = req.SessionID
		if req.Metadata != nil {
			t.Fatal("adapter must not invent Metadata")
		}
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, text+"-ok")},
		}, nil
	}}

	nodeRun := AdaptRunner(
		runner,
		func(snap Snapshot[counter]) (*schema.RunRequest, error) {
			return &schema.RunRequest{
				Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, strings.Repeat("x", Get(snap, query)))},
				SessionID: "s1",
			}, nil
		},
		func(_ Snapshot[counter], resp *schema.RunResponse) (Patch[counter], error) {
			return NewPatch(Set(out, len(resp.Messages[0].Text()))), nil
		},
	)

	wf := mustNew(t, []Node[counter]{{ID: "map", Run: nodeRun}})
	got, err := wf.Run(context.Background(), counter{A: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sawSession != "s1" {
		t.Fatalf("session %q", sawSession)
	}
	if got.B != len("xxx-ok") {
		t.Fatalf("got B=%d", got.B)
	}
}

func TestAdaptRunnerNilResponse(t *testing.T) {
	var mapped atomic.Bool
	nodeRun := AdaptRunner(
		stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, nil
		}},
		func(Snapshot[counter]) (*schema.RunRequest, error) {
			return &schema.RunRequest{}, nil
		},
		func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
			mapped.Store(true)
			return Patch[counter]{}, nil
		},
	)

	wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
	_, err := wf.Run(context.Background(), counter{})
	if !errors.Is(err, ErrNilResponse) {
		t.Fatalf("error %v, want ErrNilResponse", err)
	}
	if mapped.Load() {
		t.Fatal("output mapper ran on nil response")
	}
}

func TestAdaptRunnerMapperError(t *testing.T) {
	inErr := errors.New("bad request")
	outErr := errors.New("bad patch")

	t.Run("request mapper", func(t *testing.T) {
		nodeRun := AdaptRunner(
			stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
				t.Fatal("runner should not run")
				return nil, nil
			}},
			func(Snapshot[counter]) (*schema.RunRequest, error) { return nil, inErr },
			func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
				return Patch[counter]{}, nil
			},
		)
		wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
		_, err := wf.Run(context.Background(), counter{})
		if !errors.Is(err, inErr) {
			t.Fatalf("error %v, want inErr", err)
		}
	})

	t.Run("nil request", func(t *testing.T) {
		nodeRun := AdaptRunner(
			stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
				t.Fatal("runner should not run")
				return nil, nil
			}},
			func(Snapshot[counter]) (*schema.RunRequest, error) { return nil, nil },
			func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
				return Patch[counter]{}, nil
			},
		)
		wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
		_, err := wf.Run(context.Background(), counter{})
		if !errors.Is(err, ErrNilRequest) {
			t.Fatalf("error %v, want ErrNilRequest", err)
		}
	})

	t.Run("response mapper", func(t *testing.T) {
		nodeRun := AdaptRunner(
			stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
				return &schema.RunResponse{}, nil
			}},
			func(Snapshot[counter]) (*schema.RunRequest, error) { return &schema.RunRequest{}, nil },
			func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
				return Patch[counter]{}, outErr
			},
		)
		wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
		_, err := wf.Run(context.Background(), counter{})
		if !errors.Is(err, outErr) {
			t.Fatalf("error %v, want outErr", err)
		}
	})
}

func TestAdaptRunnerInterruptedDoesNotCallOutputMapper(t *testing.T) {
	cases := []struct {
		name string
		resp *schema.RunResponse
	}{
		{
			name: "both signals",
			resp: &schema.RunResponse{
				StopReason: schema.StopReasonInterrupted,
				Interrupt:  &schema.InterruptDescriptor{InterruptID: "int-1"},
			},
		},
		{
			name: "stop reason only",
			resp: &schema.RunResponse{StopReason: schema.StopReasonInterrupted},
		},
		{
			name: "interrupt field only",
			resp: &schema.RunResponse{Interrupt: &schema.InterruptDescriptor{InterruptID: "x"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mapped atomic.Bool
			nodeRun := AdaptRunner(
				stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
					return tc.resp, nil
				}},
				func(Snapshot[counter]) (*schema.RunRequest, error) { return &schema.RunRequest{}, nil },
				func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
					mapped.Store(true)
					return Patch[counter]{}, nil
				},
			)
			wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
			_, err := wf.Run(context.Background(), counter{})
			if !errors.Is(err, ErrInterruptedRunner) {
				t.Fatalf("error %v, want ErrInterruptedRunner", err)
			}
			if mapped.Load() {
				t.Fatal("output mapper ran on interrupted response")
			}
		})
	}
}

func TestAdaptRunnerRunnerError(t *testing.T) {
	runErr := errors.New("runner down")
	var mapped atomic.Bool
	nodeRun := AdaptRunner(
		stubRunner{fn: func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, runErr
		}},
		func(Snapshot[counter]) (*schema.RunRequest, error) { return &schema.RunRequest{}, nil },
		func(Snapshot[counter], *schema.RunResponse) (Patch[counter], error) {
			mapped.Store(true)
			return Patch[counter]{}, nil
		},
	)
	wf := mustNew(t, []Node[counter]{{ID: "n", Run: nodeRun}})
	_, err := wf.Run(context.Background(), counter{})
	if !errors.Is(err, runErr) {
		t.Fatalf("error %v, want runErr", err)
	}
	if mapped.Load() {
		t.Fatal("output mapper ran on runner error")
	}
}
