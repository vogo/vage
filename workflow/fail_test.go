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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeFailureZeroCommitsAndSkipsSuccessors(t *testing.T) {
	a := fieldA()
	var successor atomic.Int32
	boom := errors.New("boom")

	wf := mustNew(t, []Node[counter]{
		{
			ID: "fail",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				return NewPatch(Set(a, 1)), boom
			},
		},
		{
			ID:   "later",
			Deps: []string{"fail"},
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				successor.Add(1)
				return NewPatch(Set(fieldB(), 2)), nil
			},
		},
	})

	initial := counter{A: 9}
	got, err := wf.Run(context.Background(), initial)
	var nerr *NodeError
	if !errors.As(err, &nerr) || nerr.NodeID != "fail" {
		t.Fatalf("error %+v, want NodeError for fail", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not unwrap boom", err)
	}
	if got != initial {
		t.Fatalf("got %+v, want initial (batch not committed)", got)
	}
	if successor.Load() != 0 {
		t.Fatal("successor ran after a failed batch")
	}
}

func TestBatchFailureDoesNotCommitSiblingPatches(t *testing.T) {
	a, b := fieldA(), fieldB()
	var started sync.WaitGroup
	started.Add(2)
	boom := errors.New("boom")

	wf := mustNew(t, []Node[counter]{
		{
			ID: "ok",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(a, 1)), nil
			},
		},
		{
			ID: "fail",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return NewPatch(Set(b, 2)), boom
			},
		},
		{
			ID:   "join",
			Deps: []string{"ok", "fail"},
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				t.Fatal("join must not run after a failed batch")
				return Patch[counter]{}, nil
			},
		},
	})

	initial := counter{C: 7}
	got, err := wf.Run(context.Background(), initial)
	if !errors.Is(err, boom) {
		t.Fatalf("error %v, want boom", err)
	}
	if got != initial {
		t.Fatalf("got %+v, want initial", got)
	}
}

func TestCancelConvergesRunningNodes(t *testing.T) {
	var started sync.WaitGroup
	started.Add(2)
	var slowDone atomic.Bool

	wf := mustNew(t, []Node[counter]{
		{
			ID: "fast-fail",
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				return Patch[counter]{}, errors.New("stop")
			},
		},
		{
			ID: "slow",
			Run: func(ctx context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				started.Done()
				started.Wait()
				<-ctx.Done()
				slowDone.Store(true)
				return Patch[counter]{}, ctx.Err()
			},
		},
		{
			ID:   "join",
			Deps: []string{"fast-fail", "slow"},
			Run: func(_ context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				t.Fatal("join must not run after a failed batch")
				return Patch[counter]{}, nil
			},
		},
	}, WithMaxConcurrency(2))

	before := runtime.NumGoroutine()
	_, err := wf.Run(context.Background(), counter{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !slowDone.Load() {
		t.Fatal("slow node was not cancelled and collected")
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.NumGoroutine() > before+8 {
		t.Fatalf("possible goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
	}
}

func TestParentCancelPreservesCanceled(t *testing.T) {
	started := make(chan struct{})
	wf := mustNew(t, []Node[counter]{
		{
			ID: "block",
			Run: func(ctx context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				close(started)
				<-ctx.Done()
				return Patch[counter]{}, ctx.Err()
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := wf.Run(ctx, counter{A: 1})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("node did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestDeadlineExceededIdentified(t *testing.T) {
	wf := mustNew(t, []Node[counter]{
		{
			ID: "block",
			Run: func(ctx context.Context, _ Snapshot[counter]) (Patch[counter], error) {
				<-ctx.Done()
				return Patch[counter]{}, ctx.Err()
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := wf.Run(ctx, counter{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want context.DeadlineExceeded", err)
	}
}
