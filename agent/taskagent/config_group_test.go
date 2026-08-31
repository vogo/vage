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

package taskagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
)

// noopPolicy is a non-nil InterruptPolicy that never flags anything; it is
// used wherever the tests need a valid custom policy.
func noopPolicy() InterruptPolicy {
	return InterruptPolicyFunc(func(context.Context, string, []schema.ToolCall) []string { return nil })
}

func TestNewValidated_InterruptConfig(t *testing.T) {
	store := interrupt.NewMapStore()
	policy := noopPolicy()

	tests := []struct {
		name    string
		cfg     InterruptConfig
		wantErr bool
	}{
		{"disabled zero value", InterruptConfig{}, false},
		{"store + custom policy", InterruptConfig{Store: store, Policy: policy}, false},
		{"store + tool names", InterruptConfig{Store: store, ToolNames: []string{"ask_user"}}, false},
		{"store + empty tool names is explicit empty policy", InterruptConfig{Store: store, ToolNames: []string{}}, false},
		{"store only", InterruptConfig{Store: store}, true},
		{"policy only", InterruptConfig{Policy: policy}, true},
		{"tool names only", InterruptConfig{ToolNames: []string{"ask_user"}}, true},
		{"policy and tool names both set", InterruptConfig{Store: store, Policy: policy, ToolNames: []string{"ask_user"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewValidated(agent.Config{}, WithInterrupt(tt.cfg))
			if tt.wantErr {
				if !errors.Is(err, ErrInterruptConfig) {
					t.Errorf("NewValidated err = %v, want ErrInterruptConfig", err)
				}
				if a != nil {
					t.Error("NewValidated returned a non-nil agent on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewValidated err = %v, want nil", err)
			}
			if a == nil {
				t.Fatal("NewValidated returned nil agent")
			}
		})
	}
}

func TestWithInterrupt_EquivalentToIndividualOptions(t *testing.T) {
	store := interrupt.NewMapStore()

	// Store + custom policy: same store, same behavioral policy.
	grouped := New(agent.Config{}, WithInterrupt(InterruptConfig{Store: store, Policy: noopPolicy()}))
	individual := New(agent.Config{}, WithInterruptStore(store), WithInterruptPolicy(noopPolicy()))
	if grouped.interruptStore != individual.interruptStore {
		t.Errorf("interruptStore differs: %v vs %v", grouped.interruptStore, individual.interruptStore)
	}
	if got := pendingToolCallIDs(grouped.interruptPolicy); len(got) != 0 {
		t.Errorf("custom policy pending = %v, want empty", got)
	}

	// Store + tool names: same flagged subset in the same order.
	grouped = New(agent.Config{}, WithInterrupt(InterruptConfig{Store: store, ToolNames: []string{"ask_user", "confirm"}}))
	individual = New(agent.Config{}, WithInterruptStore(store), WithInterruptToolNames("ask_user", "confirm"))
	if got := pendingToolCallIDs(grouped.interruptPolicy); len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Errorf("grouped pending = %v, want [2 3]", got)
	}
	if got := pendingToolCallIDs(individual.interruptPolicy); len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Errorf("individual pending = %v, want [2 3]", got)
	}

	// Lease TTL: explicit value lands, zero keeps the default.
	grouped = New(agent.Config{}, WithInterrupt(InterruptConfig{Store: store, Policy: noopPolicy(), LeaseTTL: 90 * time.Second}))
	individual = New(agent.Config{}, WithInterruptStore(store), WithInterruptPolicy(noopPolicy()), WithInterruptLeaseTTL(90*time.Second))
	if grouped.interruptLeaseTTL != individual.interruptLeaseTTL {
		t.Errorf("interruptLeaseTTL = %v, want %v", grouped.interruptLeaseTTL, individual.interruptLeaseTTL)
	}
	grouped = New(agent.Config{}, WithInterrupt(InterruptConfig{Store: store, Policy: noopPolicy()}))
	if grouped.interruptLeaseTTL != defaultInterruptLeaseTTL {
		t.Errorf("default interruptLeaseTTL = %v, want %v", grouped.interruptLeaseTTL, defaultInterruptLeaseTTL)
	}
}

func TestWithGuards_EquivalentToIndividualOptions(t *testing.T) {
	in := []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 10})}
	out := []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 20})}
	tr := []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 30})}

	grouped := New(agent.Config{}, WithGuards(GuardsConfig{Input: in, Output: out, ToolResult: tr}))
	individual := New(agent.Config{}, WithInputGuards(in...), WithOutputGuards(out...), WithToolResultGuards(tr...))

	assertGuardListsEqual(t, "input", grouped.inputGuards, individual.inputGuards)
	assertGuardListsEqual(t, "output", grouped.outputGuards, individual.outputGuards)
	assertGuardListsEqual(t, "tool-result", grouped.toolResultGuards, individual.toolResultGuards)
}

func assertGuardListsEqual(t *testing.T, name string, got, want []guard.Guard) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s guard len = %d, want %d", name, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s guards[%d] differs", name, i)
		}
	}
}

func TestWithGuards_WholeGroupAssignment(t *testing.T) {
	in := []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 10})}
	out := []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 20})}

	// A later single-list option wins for the list it touches only.
	a := New(
		agent.Config{},
		WithGuards(GuardsConfig{Input: in, Output: out}),
		WithOutputGuards(),
	)
	if a.outputGuards != nil {
		t.Error("later WithOutputGuards() should replace the grouped output list")
	}
	if len(a.inputGuards) != 1 {
		t.Error("WithOutputGuards() should not touch the input list")
	}

	// A later WithGuards replaces the whole group, including lists its own
	// config leaves nil.
	b := New(
		agent.Config{},
		WithInputGuards(in...),
		WithGuards(GuardsConfig{Output: out}),
	)
	if len(b.inputGuards) != 0 {
		t.Error("WithGuards must replace the whole group, not merge")
	}
	if len(b.outputGuards) != 1 {
		t.Error("WithGuards should have set the output list")
	}
}

func TestNewValidated_MixedOldAndNewOptions(t *testing.T) {
	store := interrupt.NewMapStore()
	policy := noopPolicy()

	t.Run("later single option can break a valid group", func(t *testing.T) {
		_, err := NewValidated(
			agent.Config{},
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
			WithInterruptStore(nil),
		)
		if !errors.Is(err, ErrInterruptConfig) {
			t.Errorf("err = %v, want ErrInterruptConfig", err)
		}
	})

	t.Run("later single option can fix a both-sources conflict", func(t *testing.T) {
		a, err := NewValidated(
			agent.Config{},
			WithInterrupt(InterruptConfig{Store: store, Policy: policy, ToolNames: []string{"ask_user"}}),
			WithInterruptPolicy(policy),
		)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if a.interruptStore != store {
			t.Error("interruptStore not preserved")
		}
	})

	t.Run("later tool names can break a valid group into policy-only", func(t *testing.T) {
		_, err := NewValidated(
			agent.Config{},
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
			WithInterruptStore(nil),
			WithInterruptToolNames("ask_user"),
		)
		if !errors.Is(err, ErrInterruptConfig) {
			t.Errorf("err = %v, want ErrInterruptConfig (store cleared, policy remains)", err)
		}
	})

	t.Run("group resets fields set by earlier single options", func(t *testing.T) {
		a, err := NewValidated(
			agent.Config{},
			WithInterruptStore(store),
			WithInterruptToolNames("ask_user"),
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
		)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got := pendingToolCallIDs(a.interruptPolicy); len(got) != 0 {
			t.Errorf("pending = %v, want empty (custom policy must replace the tool-names policy)", got)
		}
	})

	t.Run("group with zero value disables interrupt", func(t *testing.T) {
		a, err := NewValidated(
			agent.Config{},
			WithInterruptLeaseTTL(30*time.Second),
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
			WithInterrupt(InterruptConfig{}),
		)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if a.interruptStore != nil || a.interruptPolicy != nil {
			t.Errorf("interrupt should be disabled, store=%v policy=%v", a.interruptStore, a.interruptPolicy)
		}
		if a.interruptLeaseTTL != defaultInterruptLeaseTTL {
			t.Errorf("interruptLeaseTTL = %v, want default %v (empty group resets lease too)", a.interruptLeaseTTL, defaultInterruptLeaseTTL)
		}
	})

	t.Run("later group with unset LeaseTTL resets to default", func(t *testing.T) {
		a, _ := NewValidated(
			agent.Config{},
			WithInterruptLeaseTTL(30*time.Second),
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
		)
		if a.interruptLeaseTTL != defaultInterruptLeaseTTL {
			t.Errorf("interruptLeaseTTL = %v, want default %v (whole-group assignment resets unset LeaseTTL)", a.interruptLeaseTTL, defaultInterruptLeaseTTL)
		}

		b, _ := NewValidated(
			agent.Config{},
			WithInterruptLeaseTTL(30*time.Second),
			WithInterrupt(InterruptConfig{Store: store, Policy: policy, LeaseTTL: 90 * time.Second}),
		)
		if b.interruptLeaseTTL != 90*time.Second {
			t.Errorf("interruptLeaseTTL = %v, want 90s (later group lease wins)", b.interruptLeaseTTL)
		}
	})
}

func TestNewValidated_ValidMatchesNew(t *testing.T) {
	opts := []Option{
		WithCaller(newMock(stopResponse("hello"))),
		WithModel("gpt-4o"),
		WithInterrupt(InterruptConfig{Store: interrupt.NewMapStore(), Policy: noopPolicy(), LeaseTTL: 90 * time.Second}),
		WithGuards(GuardsConfig{Input: []guard.Guard{guard.NewLengthGuard(guard.LengthConfig{MaxLength: 100})}}),
	}

	nv, err := NewValidated(agent.Config{ID: "a", Name: "b"}, opts...)
	if err != nil {
		t.Fatalf("NewValidated err = %v, want nil", err)
	}
	n := New(agent.Config{ID: "a", Name: "b"}, opts...)

	if nv.ID() != n.ID() || nv.Name() != n.Name() || nv.Protocol() != n.Protocol() {
		t.Errorf("identity/protocol diverged: (%q, %q, %q) vs (%q, %q, %q)",
			nv.ID(), nv.Name(), nv.Protocol(), n.ID(), n.Name(), n.Protocol())
	}
	if nv.interruptStore != n.interruptStore || nv.interruptLeaseTTL != n.interruptLeaseTTL {
		t.Errorf("interrupt state diverged: store=%v/%v lease=%v/%v",
			nv.interruptStore, n.interruptStore, nv.interruptLeaseTTL, n.interruptLeaseTTL)
	}
	if len(nv.inputGuards) != len(n.inputGuards) {
		t.Errorf("guard state diverged: %d vs %d", len(nv.inputGuards), len(n.inputGuards))
	}
}

// pendingToolCallIDs runs p against a batch that contains every name the
// tool-name policy tests care about, so the two constructions can be
// compared behaviorally (interface == would panic on func-typed policies).
func pendingToolCallIDs(p InterruptPolicy) []string {
	calls := []schema.ToolCall{
		{ID: "1", Name: "echo"},
		{ID: "2", Name: "ask_user"},
		{ID: "3", Name: "confirm"},
		{ID: "4", Name: "other"},
	}
	return p.Intercept(context.Background(), "sess", calls)
}

// noTouchStore fails the test if any store method is reached; neither
// constructor may touch the store.
type noTouchStore struct {
	interrupt.Store
	t *testing.T
}

func (s noTouchStore) Create(context.Context, *interrupt.Record) error {
	s.t.Fatal("store.Create called during construction")
	return nil
}

func (s noTouchStore) Get(context.Context, string) (*interrupt.Record, error) {
	s.t.Fatal("store.Get called during construction")
	return nil, nil
}

func (s noTouchStore) SubmitDecisions(context.Context, string, []interrupt.Decision) (*interrupt.Record, []string, error) {
	s.t.Fatal("store.SubmitDecisions called during construction")
	return nil, nil, nil
}

func (s noTouchStore) AcquireLease(context.Context, string, string, time.Duration) (*interrupt.Record, error) {
	s.t.Fatal("store.AcquireLease called during construction")
	return nil, nil
}

func (s noTouchStore) ReleaseLease(context.Context, string, string) error {
	s.t.Fatal("store.ReleaseLease called during construction")
	return nil
}

func (s noTouchStore) Complete(context.Context, string, string) error {
	s.t.Fatal("store.Complete called during construction")
	return nil
}

func (s noTouchStore) List(context.Context, string) ([]*interrupt.Meta, error) {
	s.t.Fatal("store.List called during construction")
	return nil, nil
}

func (s noTouchStore) Delete(context.Context, string) error {
	s.t.Fatal("store.Delete called during construction")
	return nil
}

func TestNewValidated_NoIO(t *testing.T) {
	store := noTouchStore{t: t}
	mock := newMock()

	t.Run("invalid config returns the error before any I/O", func(t *testing.T) {
		a, err := NewValidated(
			agent.Config{},
			WithCaller(mock),
			WithInterrupt(InterruptConfig{Store: store}),
		)
		if !errors.Is(err, ErrInterruptConfig) {
			t.Errorf("err = %v, want ErrInterruptConfig", err)
		}
		if a != nil {
			t.Error("returned a non-nil agent on error")
		}
		if mock.Calls() != 0 {
			t.Errorf("caller invoked %d times during construction", mock.Calls())
		}
	})

	t.Run("valid config performs no I/O either", func(t *testing.T) {
		a, err := NewValidated(
			agent.Config{},
			WithCaller(mock),
			WithInterrupt(InterruptConfig{Store: store, Policy: noopPolicy()}),
		)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if a == nil {
			t.Fatal("NewValidated returned nil agent")
		}
		if mock.Calls() != 0 {
			t.Errorf("caller invoked %d times during construction", mock.Calls())
		}
	})
}

func TestQuickValidated(t *testing.T) {
	store := interrupt.NewMapStore()
	policy := noopPolicy()

	t.Run("valid configuration succeeds", func(t *testing.T) {
		a, err := QuickValidated(
			quickID, quickName, newMock(stopResponse("hello")), quickModel, quickPrompt,
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
		)
		if err != nil {
			t.Fatalf("QuickValidated err = %v, want nil", err)
		}
		if a.ID() != quickID || a.Name() != quickName {
			t.Errorf("identity = (%q, %q), want (%q, %q)", a.ID(), a.Name(), quickID, quickName)
		}
		if a.model != quickModel {
			t.Errorf("model = %q, want %q", a.model, quickModel)
		}
		if a.interruptStore != store {
			t.Error("interruptStore not set")
		}
	})

	t.Run("invalid configuration returns ErrInterruptConfig and nil agent", func(t *testing.T) {
		a, err := QuickValidated(
			quickID, quickName, newMock(), quickModel, quickPrompt,
			WithInterrupt(InterruptConfig{Store: store}),
		)
		if !errors.Is(err, ErrInterruptConfig) {
			t.Errorf("err = %v, want ErrInterruptConfig", err)
		}
		if a != nil {
			t.Error("returned a non-nil agent on error")
		}
	})

	t.Run("matches the equivalent NewValidated expansion", func(t *testing.T) {
		q, err := QuickValidated(
			quickID, quickName, newMock(), quickModel, quickPrompt,
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
		)
		if err != nil {
			t.Fatalf("QuickValidated err = %v", err)
		}
		n, err := NewValidated(
			agent.Config{ID: quickID, Name: quickName},
			WithCaller(newMock()),
			WithModel(quickModel),
			WithSystemPrompt(prompt.StringPrompt(quickPrompt)),
			WithInterrupt(InterruptConfig{Store: store, Policy: policy}),
		)
		if err != nil {
			t.Fatalf("NewValidated err = %v", err)
		}
		if q.ID() != n.ID() || q.Name() != n.Name() || q.Protocol() != n.Protocol() {
			t.Errorf("identity/protocol = (%q, %q, %q), want (%q, %q, %q)",
				q.ID(), q.Name(), q.Protocol(), n.ID(), n.Name(), n.Protocol())
		}
		if q.model != n.model || q.maxIterations != n.maxIterations {
			t.Errorf("model/maxIterations = (%q, %d), want (%q, %d)",
				q.model, q.maxIterations, n.model, n.maxIterations)
		}
	})
}
