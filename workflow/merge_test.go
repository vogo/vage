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
	"sync"
	"testing"
	"time"
)

func assertConflict(t *testing.T, err error, field string, nodes ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected write conflict")
	}
	if !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("error %v does not wrap ErrWriteConflict", err)
	}
	var conflict *WriteConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not WriteConflictError", err)
	}
	if conflict.Field != field {
		t.Fatalf("field %q, want %q", conflict.Field, field)
	}
	if len(conflict.Nodes) != len(nodes) {
		t.Fatalf("nodes %v, want %v", conflict.Nodes, nodes)
	}
	for i, n := range nodes {
		if conflict.Nodes[i] != n {
			t.Fatalf("nodes %v, want %v", conflict.Nodes, nodes)
		}
	}
}

func TestParallelDistinctFieldsCommitAtomically(t *testing.T) {
	a, b := fieldA(), fieldB()
	var started sync.WaitGroup
	started.Add(2)

	wf := mustNew(t, []Node[counter]{
		{
			ID: "left",
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				if Get(snap, a) != 0 || Get(snap, b) != 0 {
					t.Errorf("left saw committed writes in the same batch: %+v", snap.state)
				}
				return NewPatch(Set(a, 1)), nil
			},
		},
		{
			ID: "right",
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				if Get(snap, a) != 0 || Get(snap, b) != 0 {
					t.Errorf("right saw committed writes in the same batch: %+v", snap.state)
				}
				return NewPatch(Set(b, 2)), nil
			},
		},
		{
			ID:   "join",
			Deps: []string{"left", "right"},
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				if Get(snap, a) != 1 || Get(snap, b) != 2 {
					return Patch[counter]{}, errors.New("next batch did not see both committed writes")
				}
				return NewPatch(Set(fieldC(), Get(snap, a)+Get(snap, b))), nil
			},
		},
	})

	got, err := wf.Run(context.Background(), counter{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != (counter{A: 1, B: 2, C: 3}) {
		t.Fatalf("got %+v", got)
	}
}

func TestParallelSameFieldConflictsAndKeepsPriorState(t *testing.T) {
	a := fieldA()
	var started sync.WaitGroup
	started.Add(2)

	wf := mustNew(t, []Node[counter]{
		{
			ID: "alpha",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(a, 1)), nil
			},
		},
		{
			ID: "beta",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(a, 2)), nil
			},
		},
		{
			ID:   "join",
			Deps: []string{"alpha", "beta"},
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				t.Fatal("join must not run after a conflict")
				return Patch[counter]{}, nil
			},
		},
	})

	initial := counter{A: 9, B: 8}
	got, err := wf.Run(context.Background(), initial)
	assertConflict(t, err, "a", "alpha", "beta")
	if got != initial {
		t.Fatalf("state after conflict %+v, want initial %+v", got, initial)
	}
}

func TestConflictIndependentOfCompletionOrderAndConcurrency(t *testing.T) {
	a, b := fieldA(), fieldB()

	runWithOrder := func(t *testing.T, slowID string, conc int) (counter, error) {
		t.Helper()
		wf := mustNew(t, []Node[counter]{
			{
				ID: "alpha",
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					if slowID == "alpha" {
						time.Sleep(20 * time.Millisecond)
					}
					return NewPatch(Set(a, 1)), nil
				},
			},
			{
				ID: "beta",
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					if slowID == "beta" {
						time.Sleep(20 * time.Millisecond)
					}
					return NewPatch(Set(b, 2)), nil
				},
			},
			{
				ID:   "join",
				Deps: []string{"alpha", "beta"},
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					return Patch[counter]{}, nil
				},
			},
		}, WithMaxConcurrency(conc))
		return wf.Run(context.Background(), counter{})
	}

	for _, slow := range []string{"alpha", "beta"} {
		for _, conc := range []int{0, 1, 4} {
			got, err := runWithOrder(t, slow, conc)
			if err != nil {
				t.Fatalf("slow=%s conc=%d: %v", slow, conc, err)
			}
			if got != (counter{A: 1, B: 2}) {
				t.Fatalf("slow=%s conc=%d got %+v", slow, conc, got)
			}
		}
	}
}

func TestConflictIndependentOfCompletionOrderOnSameField(t *testing.T) {
	a := fieldA()

	runWithOrder := func(t *testing.T, slowID string, conc int) (counter, error) {
		t.Helper()
		wf := mustNew(t, []Node[counter]{
			{
				ID: "alpha",
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					if slowID == "alpha" {
						time.Sleep(20 * time.Millisecond)
					}
					return NewPatch(Set(a, 1)), nil
				},
			},
			{
				ID: "beta",
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					if slowID == "beta" {
						time.Sleep(20 * time.Millisecond)
					}
					return NewPatch(Set(a, 2)), nil
				},
			},
			{
				ID:   "join",
				Deps: []string{"alpha", "beta"},
				Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
					t.Fatal("join must not run after a conflict")
					return Patch[counter]{}, nil
				},
			},
		}, WithMaxConcurrency(conc))
		return wf.Run(context.Background(), counter{A: 5})
	}

	for _, slow := range []string{"alpha", "beta"} {
		for _, conc := range []int{1, 2} {
			got, err := runWithOrder(t, slow, conc)
			assertConflict(t, err, "a", "alpha", "beta")
			if got.A != 5 {
				t.Fatalf("slow=%s conc=%d state %+v", slow, conc, got)
			}
		}
	}
}

func TestSameBatchCannotSeeSiblingPatch(t *testing.T) {
	a, b := fieldA(), fieldB()
	var started sync.WaitGroup
	started.Add(2)

	wf := mustNew(t, []Node[counter]{
		{
			ID: "writer",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(a, 99)), nil
			},
		},
		{
			ID: "reader",
			Run: func(_ context.Context, snap Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(b, Get(snap, a))), nil
			},
		},
		{
			ID:   "join",
			Deps: []string{"writer", "reader"},
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				return Patch[counter]{}, nil
			},
		},
	})

	got, err := wf.Run(context.Background(), counter{A: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.A != 99 || got.B != 3 {
		t.Fatalf("got %+v, want A=99 B=3 (reader used pre-batch snapshot)", got)
	}
}

func TestDuplicateWriteInOnePatchIsZeroCommit(t *testing.T) {
	a := fieldA()
	wf := mustNew(t, []Node[counter]{
		{
			ID: "dup",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				return NewPatch(Set(a, 1), Set(a, 2)), nil
			},
		},
	})

	initial := counter{A: 4}
	got, err := wf.Run(context.Background(), initial)
	assertConflict(t, err, "a", "dup")
	if got != initial {
		t.Fatalf("got %+v, want initial", got)
	}
}
