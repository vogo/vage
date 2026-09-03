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
	"fmt"
	"strings"
	"testing"
)

type counter struct {
	A int
	B int
	C int
}

func fieldA() Field[counter, int] {
	return NewField(
		"a",
		func(s counter) int { return s.A },
		func(s *counter, v int) { s.A = v },
	)
}

func fieldB() Field[counter, int] {
	return NewField(
		"b",
		func(s counter) int { return s.B },
		func(s *counter, v int) { s.B = v },
	)
}

func fieldC() Field[counter, int] {
	return NewField(
		"c",
		func(s counter) int { return s.C },
		func(s *counter, v int) { s.C = v },
	)
}

func mustNew[S any](t *testing.T, nodes []Node[S], opts ...Option) *Workflow[S] {
	t.Helper()
	wf, err := New(nodes, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return wf
}

func writeNode[S, V any](id string, deps []string, field Field[S, V], value V) Node[S] {
	return Node[S]{
		ID:   id,
		Deps: deps,
		Run: func(_ context.Context, _ Snapshot[S]) (Patch[S], error) {
			return NewPatch(Set(field, value)), nil
		},
	}
}

func TestNewFieldPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewField("", func(s counter) int { return s.A }, func(s *counter, v int) { s.A = v })
}

func TestNewFieldPanicsOnNilGetterOrSetter(t *testing.T) {
	t.Run("nil getter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		NewField("a", nil, func(s *counter, v int) { s.A = v })
	})
	t.Run("nil setter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		NewField("a", func(s counter) int { return s.A }, nil)
	})
}

func TestEmptyGraphReturnsInitial(t *testing.T) {
	wf := mustNew[counter](t, nil)
	got, err := wf.Run(context.Background(), counter{A: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != (counter{A: 7}) {
		t.Fatalf("got %+v, want initial", got)
	}
}

func TestLinearPatchesCompose(t *testing.T) {
	a, b, c := fieldA(), fieldB(), fieldC()
	wf := mustNew(t, []Node[counter]{
		{
			ID: "n1",
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				return NewPatch(Set(a, Get(snap, a)+1)), nil
			},
		},
		{
			ID:   "n2",
			Deps: []string{"n1"},
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				return NewPatch(Set(b, Get(snap, a)*10)), nil
			},
		},
		{
			ID:   "n3",
			Deps: []string{"n2"},
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				return NewPatch(Set(c, Get(snap, a)+Get(snap, b))), nil
			},
		},
	})

	got, err := wf.Run(context.Background(), counter{A: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := counter{A: 4, B: 40, C: 44}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEmptyPatchIsReadOnly(t *testing.T) {
	a := fieldA()
	wf := mustNew(t, []Node[counter]{
		{
			ID: "peek",
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				if Get(snap, a) != 5 {
					return Patch[counter]{}, fmt.Errorf("snapshot a=%d", Get(snap, a))
				}
				return NewPatch[counter](), nil
			},
		},
		writeNode("set", []string{"peek"}, a, 9),
	})

	got, err := wf.Run(context.Background(), counter{A: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.A != 9 {
		t.Fatalf("got A=%d, want 9", got.A)
	}
}

func TestSequentialBatchesMayWriteSameField(t *testing.T) {
	a := fieldA()
	wf := mustNew(t, []Node[counter]{
		writeNode("first", nil, a, 1),
		writeNode("second", []string{"first"}, a, 2),
	})

	got, err := wf.Run(context.Background(), counter{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.A != 2 {
		t.Fatalf("got A=%d, want 2", got.A)
	}
}

func TestGraphValidation(t *testing.T) {
	run := func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
		return Patch[counter]{}, nil
	}

	cases := []struct {
		name  string
		nodes []Node[counter]
		want  string
	}{
		{
			name:  "empty id",
			nodes: []Node[counter]{{ID: "", Run: run}},
			want:  "empty node ID",
		},
		{
			name:  "nil run",
			nodes: []Node[counter]{{ID: "a"}},
			want:  "nil run function",
		},
		{
			name: "duplicate id",
			nodes: []Node[counter]{
				{ID: "a", Run: run},
				{ID: "a", Run: run},
			},
			want: "duplicate node ID",
		},
		{
			name: "unknown dep",
			nodes: []Node[counter]{
				{ID: "a", Deps: []string{"missing"}, Run: run},
			},
			want: "unknown node",
		},
		{
			name: "cycle",
			nodes: []Node[counter]{
				{ID: "a", Deps: []string{"b"}, Run: run},
				{ID: "b", Deps: []string{"a"}, Run: run},
			},
			want: "cycle",
		},
		{
			name: "disconnected",
			nodes: []Node[counter]{
				{ID: "a", Run: run},
				{ID: "b", Run: run},
			},
			want: "disconnected",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.nodes)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestNegativeConcurrencyRejected(t *testing.T) {
	_, err := New([]Node[counter]{
		{ID: "a", Run: func(context.Context, Snapshot[counter]) (Patch[counter], error) {
			return Patch[counter]{}, nil
		}},
	}, WithMaxConcurrency(-1))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCanceledContextBeforeRun(t *testing.T) {
	wf := mustNew(t, []Node[counter]{
		writeNode("a", nil, fieldA(), 1),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := wf.Run(ctx, counter{A: 3})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context.Canceled", err)
	}
	if got.A != 3 {
		t.Fatalf("got %+v, want initial", got)
	}
}

func TestNilWorkflowRun(t *testing.T) {
	var wf *Workflow[counter]
	_, err := wf.Run(context.Background(), counter{})
	if err == nil {
		t.Fatal("expected error")
	}
}
