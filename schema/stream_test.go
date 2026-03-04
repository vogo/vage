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

package schema

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestRunStream_BasicSendRecv(t *testing.T) {
	rs := NewRunStream(context.Background(), 4, func(_ context.Context, send func(Event) error) error {
		for i := range 3 {
			types := []string{EventAgentStart, EventTextDelta, EventAgentEnd}
			if err := send(NewEvent(types[i], "a", "s", nil)); err != nil {
				return err
			}
		}
		return nil
	})

	expected := []string{EventAgentStart, EventTextDelta, EventAgentEnd}
	for _, want := range expected {
		e, err := rs.Recv()
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		if e.Type != want {
			t.Errorf("Type = %q, want %q", e.Type, want)
		}
	}

	// After all events, should get io.EOF.
	_, err := rs.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("final Recv error = %v, want io.EOF", err)
	}
}

func TestRunStream_ProducerError(t *testing.T) {
	producerErr := errors.New("something broke")
	rs := NewRunStream(context.Background(), 4, func(_ context.Context, send func(Event) error) error {
		_ = send(NewEvent(EventAgentStart, "", "", nil))
		return producerErr
	})

	// First event should succeed.
	e, err := rs.Recv()
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}
	if e.Type != EventAgentStart {
		t.Errorf("Type = %q, want %q", e.Type, EventAgentStart)
	}

	// Next Recv should return the producer error.
	_, err = rs.Recv()
	if !errors.Is(err, producerErr) {
		t.Errorf("error = %v, want %v", err, producerErr)
	}
}

func TestRunStream_CloseStopsProducer(t *testing.T) {
	sent := make(chan int, 100)

	rs := NewRunStream(context.Background(), 2, func(ctx context.Context, send func(Event) error) error {
		for i := range 100 {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			if err := send(NewEvent(EventTextDelta, "", "", nil)); err != nil {
				return nil // producer stops cleanly on ErrRunStreamClosed
			}
			sent <- i
		}
		return nil
	})

	// Read one event.
	_, err := rs.Recv()
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}

	// Close the stream.
	if err := rs.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Subsequent Recv should return ErrRunStreamClosed.
	_, err = rs.Recv()
	if !errors.Is(err, ErrRunStreamClosed) {
		t.Errorf("Recv after close error = %v, want ErrRunStreamClosed", err)
	}
}

func TestRunStream_RecvAfterClose(t *testing.T) {
	rs := NewRunStream(context.Background(), 4, func(_ context.Context, send func(Event) error) error {
		return send(NewEvent(EventAgentStart, "", "", nil))
	})

	_ = rs.Close()

	_, err := rs.Recv()
	if !errors.Is(err, ErrRunStreamClosed) {
		t.Errorf("error = %v, want ErrRunStreamClosed", err)
	}
}

func TestRunStream_EmptyStream(t *testing.T) {
	rs := NewRunStream(context.Background(), 4, func(_ context.Context, _ func(Event) error) error {
		return nil
	})

	_, err := rs.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("error = %v, want io.EOF", err)
	}
}

func TestRunStream_DoubleClose(t *testing.T) {
	rs := NewRunStream(context.Background(), 4, func(_ context.Context, _ func(Event) error) error {
		return nil
	})

	if err := rs.Close(); err != nil {
		t.Errorf("first Close error: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Errorf("second Close error: %v", err)
	}
}

func TestRunStream_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	producerDone := make(chan struct{})
	rs := NewRunStream(ctx, 4, func(ctx context.Context, send func(Event) error) error {
		defer close(producerDone)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			if err := send(NewEvent(EventTextDelta, "", "", nil)); err != nil {
				return err
			}
		}
	})

	// Read one event to confirm the stream works.
	_, err := rs.Recv()
	if err != nil {
		t.Fatalf("Recv error: %v", err)
	}

	// Cancel the parent context.
	cancel()

	// Producer should stop.
	<-producerDone

	// Close the stream.
	_ = rs.Close()
}

func TestMergeStreams(t *testing.T) {
	ctx := context.Background()

	s1 := NewRunStream(ctx, 4, func(_ context.Context, send func(Event) error) error {
		return send(NewEvent(EventTextDelta, "a1", "", TextDeltaData{Delta: "hello"}))
	})

	s2 := NewRunStream(ctx, 4, func(_ context.Context, send func(Event) error) error {
		return send(NewEvent(EventTextDelta, "a2", "", TextDeltaData{Delta: "world"}))
	})

	merged := MergeStreams(ctx, 8, s1, s2)

	var events []Event
	for {
		e, err := merged.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	agents := map[string]bool{}
	for _, e := range events {
		agents[e.AgentID] = true
	}
	if !agents["a1"] || !agents["a2"] {
		t.Errorf("expected events from both a1 and a2, got agents: %v", agents)
	}
}
