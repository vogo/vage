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

package orchestrate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/largemodel/schema"
)

func TestResourceManager_ConcurrencyLimits(t *testing.T) {
	rm := newResourceManager(map[string]int{"gpu": 1}, nil)
	ctx := context.Background()

	// Acquire first slot.
	if err := rm.acquire(ctx, []string{"gpu"}); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Second acquire should block. Use a timeout context.
	ctxTimeout, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	err := rm.acquire(ctxTimeout, []string{"gpu"})
	if err == nil {
		t.Fatal("expected timeout error for second acquire")
	}

	// Release first, then acquire should succeed.
	rm.release([]string{"gpu"})
	if err := rm.acquire(ctx, []string{"gpu"}); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	rm.release([]string{"gpu"})
}

func TestResourceManager_RateLimits(t *testing.T) {
	// Rate limit of 10/s should allow burst but eventually limit.
	rm := newResourceManager(nil, map[string]float64{"api": 10.0})
	ctx := context.Background()

	// First few should succeed quickly (burst).
	start := time.Now()
	for i := range 5 {
		if err := rm.acquire(ctx, []string{"api"}); err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// Should complete quickly due to burst capacity.
	if elapsed > 2*time.Second {
		t.Errorf("burst should be fast, took %v", elapsed)
	}
}

func TestResourceManager_NilManager(t *testing.T) {
	var rm *resourceManager
	// Should not panic.
	if err := rm.acquire(context.Background(), []string{"gpu"}); err != nil {
		t.Fatalf("nil manager should not error: %v", err)
	}
	rm.release([]string{"gpu"})
}

func TestResourceManager_EmptyTags(t *testing.T) {
	rm := newResourceManager(map[string]int{"gpu": 1}, nil)
	// Empty tags should be a no-op.
	if err := rm.acquire(context.Background(), nil); err != nil {
		t.Fatalf("empty tags should not error: %v", err)
	}
	rm.release(nil)
}

func TestResourceManager_AcquireOrderIndependent(t *testing.T) {
	// Two goroutines acquire overlapping tags in opposite order.
	// Without sorted acquisition, this could deadlock.
	rm := newResourceManager(map[string]int{"alpha": 1, "beta": 1}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := rm.acquire(ctx, []string{"beta", "alpha"}); err != nil {
			errs <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		rm.release([]string{"beta", "alpha"})
	}()
	go func() {
		defer wg.Done()
		if err := rm.acquire(ctx, []string{"alpha", "beta"}); err != nil {
			errs <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		rm.release([]string{"alpha", "beta"})
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("deadlock or error: %v", err)
	}
}

func TestDAG_ResourceLimits(t *testing.T) {
	var maxConcurrentGPU atomic.Int32
	var currentGPU atomic.Int32

	gpuRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cur := currentGPU.Add(1)
		for {
			old := maxConcurrentGPU.Load()
			if cur <= old || maxConcurrentGPU.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		currentGPU.Add(-1)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
		{ID: "B", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
		{ID: "C", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
	}

	cfg := DAGConfig{
		ResourceLimits: map[string]int{"gpu": 1},
	}

	result, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"A", "B", "C"} {
		if result.NodeStatus[id] != NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}

	if maxConcurrentGPU.Load() > 1 {
		t.Errorf("max concurrent GPU = %d, want <= 1", maxConcurrentGPU.Load())
	}
}
