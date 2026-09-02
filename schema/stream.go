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
	"sync"
	"sync/atomic"
)

// ErrRunStreamClosed is returned when Recv is called on a closed RunStream.
var ErrRunStreamClosed = errors.New("vage: run stream is closed")

// StreamProducer is the function signature for producing stream events.
// The producer receives a context (canceled on Close) and a send function to emit events.
type StreamProducer func(ctx context.Context, send func(Event) error) error

// RunStream delivers streaming events from an agent run using a pull-based API.
// The caller reads events via Recv and can stop the stream early via Close.
type RunStream struct {
	ch     chan Event
	done   chan struct{} // closed when producer finishes
	err    atomic.Value  // stores the producer error (error or nil)
	closed atomic.Bool
	cancel context.CancelFunc
}

// NewRunStream creates a RunStream and starts the producer in a goroutine.
// The producer calls send to emit events. If the producer returns a non-nil
// error, Recv will surface it after all buffered events are drained.
// The parent context is used to derive a cancelable context for the producer;
// calling Close cancels this context, allowing the producer to stop promptly.
func NewRunStream(ctx context.Context, bufSize int, producer StreamProducer) *RunStream {
	ctx, cancel := context.WithCancel(ctx)

	rs := &RunStream{
		ch:     make(chan Event, bufSize),
		done:   make(chan struct{}),
		cancel: cancel,
	}

	go func() {
		defer close(rs.done)
		defer close(rs.ch)

		err := producer(ctx, func(e Event) error {
			if rs.closed.Load() {
				return ErrRunStreamClosed
			}
			select {
			case rs.ch <- e:
				return nil
			case <-ctx.Done():
				return ErrRunStreamClosed
			}
		})
		if err != nil {
			rs.err.Store(err)
		}
	}()

	return rs
}

// Recv returns the next event from the stream.
// Returns io.EOF when the stream completes successfully.
// Returns the producer error if the producer failed.
// Returns ErrRunStreamClosed if Close was called.
func (rs *RunStream) Recv() (Event, error) {
	if rs.closed.Load() {
		return Event{}, ErrRunStreamClosed
	}

	e, ok := <-rs.ch
	if ok {
		return e, nil
	}

	// Channel closed -- producer is done.
	if v := rs.err.Load(); v != nil {
		return Event{}, v.(error)
	}

	return Event{}, io.EOF
}

// Close signals the producer to stop and prevents further Recv calls.
func (rs *RunStream) Close() error {
	if rs.closed.CompareAndSwap(false, true) {
		rs.cancel()
	}

	return nil
}

// MergeStreams merges multiple RunStreams into a single RunStream.
// Events from all source streams are interleaved. The parentID is set on each
// forwarded event to track which sub-stream produced it.
func MergeStreams(ctx context.Context, bufSize int, streams ...*RunStream) *RunStream {
	return NewRunStream(ctx, bufSize, func(ctx context.Context, send func(Event) error) error {
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			errs []error
		)

		for _, s := range streams {
			wg.Add(1)

			go func(s *RunStream) {
				defer wg.Done()

				for {
					select {
					case <-ctx.Done():
						return
					default:
					}

					e, err := s.Recv()
					if err != nil {
						if !errors.Is(err, io.EOF) {
							mu.Lock()
							errs = append(errs, err)
							mu.Unlock()
						}

						return
					}

					if sendErr := send(e); sendErr != nil {
						return
					}
				}
			}(s)
		}

		wg.Wait()

		if len(errs) > 0 {
			return errors.Join(errs...)
		}

		return nil
	})
}

// ForEach drains rs to completion, calling fn for every event in arrival
// order, and closes rs before returning. It is the shared form of the
// "loop until io.EOF, dispatch on Event.Data" block every stream consumer
// would otherwise hand-roll.
//
// The normal end of a stream (io.EOF) is consumed and reported as a nil
// error — reaching the end is success, not a failure the caller must
// classify. Any other Recv error is returned as-is, so producer errors keep
// their type and stay inspectable with errors.Is/errors.As.
//
// A non-nil error from fn stops the drain immediately and is returned
// unchanged. The stream is still closed, but the events after the stopping
// one are never delivered: fn's error means "I am done reading", so use it
// for early exit as well as for failure.
func (rs *RunStream) ForEach(fn func(Event) error) error {
	defer func() { _ = rs.Close() }()

	for {
		e, err := rs.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		if err := fn(e); err != nil {
			return err
		}
	}
}
