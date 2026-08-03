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

package largemodel

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/vogo/vage/schema"
)

// ErrStreamClosed reports a read from a stream that has already been closed.
var ErrStreamClosed = errors.New("vage: stream is closed")

// Stream delivers a model response incrementally. It wraps a protocol-specific
// decoder with the lifecycle guarantees every caller depends on: Recv returns
// io.EOF exactly once at the end, Close runs the underlying release exactly
// once no matter how many times it is called, and the final Usage the vendor
// reports is captured as it flows past.
//
// A Stream is safe for concurrent use between one Recv caller and Close, so a
// reader can be unblocked by closing from another goroutine.
type Stream struct {
	mu sync.Mutex

	// recv pulls the next chunk from the protocol-specific decoder.
	recv func() (*Chunk, error)

	// release closes the underlying response body. It runs exactly once.
	release func() error

	closed atomic.Bool

	// usage retains the last usage the vendor reported, which arrives on or
	// near the terminal chunk. It is held atomically rather than under mu
	// because Close reads it while a Recv may be blocked on a network read
	// still holding mu — taking mu here would deadlock the two.
	usage atomic.Pointer[schema.Usage]

	// onClose, when set, fires once with the final usage as the stream
	// closes. Middlewares use it to record accounting exactly once, whether
	// the stream was drained or abandoned.
	onClose func(*schema.Usage)
}

// NewStream builds a Stream from a decoder and its release function. recv must
// return io.EOF when the response is complete; release must be safe to call
// once and is invoked by Close.
func NewStream(recv func() (*Chunk, error), release func() error) *Stream {
	return &Stream{recv: recv, release: release}
}

// Recv returns the next chunk, or io.EOF when the response is complete.
// Reading a closed stream returns ErrStreamClosed.
func (s *Stream) Recv() (*Chunk, error) {
	if s.closed.Load() {
		return nil, ErrStreamClosed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-check under the lock: Close may have won the race after the
	// unsynchronized check above.
	if s.closed.Load() {
		return nil, ErrStreamClosed
	}

	chunk, err := s.recv()
	if chunk != nil && chunk.Usage != nil {
		s.usage.Store(chunk.Usage)
	}

	return chunk, err
}

// Usage returns the final token accounting the vendor reported, or nil when
// the stream ended without reporting any.
//
// It takes no lock, so it stays callable from Close while a Recv is in flight.
func (s *Stream) Usage() *schema.Usage {
	return s.usage.Load()
}

// Close releases the stream's resources. It is idempotent and safe to call
// concurrently with Recv — closing the underlying body unblocks a Recv already
// in flight, which is why neither the release nor the onClose callback takes
// the mutex the blocked Recv is holding.
func (s *Stream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}

	if s.onClose != nil {
		s.onClose(s.Usage())
	}

	if s.release == nil {
		return nil
	}

	return s.release()
}

// WrapStreamClose registers a callback that fires once when s closes, carrying
// whatever usage the stream captured. It is how middlewares record accounting
// exactly once for a stream whose consumer may abandon it early.
//
// A nil stream invokes onClose immediately with nil usage and returns nil, so
// callers can wrap an error path without a nil check.
func WrapStreamClose(s *Stream, onClose func(*schema.Usage)) *Stream {
	if s == nil {
		if onClose != nil {
			onClose(nil)
		}

		return nil
	}

	prev := s.onClose

	s.onClose = func(u *schema.Usage) {
		if prev != nil {
			prev(u)
		}

		if onClose != nil {
			onClose(u)
		}
	}

	return s
}

// InterceptStream returns a Stream that reports every chunk to onChunk as it
// is read, and calls onDone exactly once when the stream finishes — whether it
// was drained to completion, failed, or was closed early by its consumer.
//
// It is how observability layers watch a stream without consuming it: the
// caller still drives Recv and still owns Close.
//
// A nil stream invokes onDone immediately with nil and returns nil, so callers
// can wrap an error path without a nil check.
func InterceptStream(s *Stream, onChunk func(*Chunk), onDone func(error)) *Stream {
	if s == nil {
		if onDone != nil {
			onDone(nil)
		}

		return nil
	}

	// done guards against reporting completion twice, since the stream can
	// finish either by reaching its end in Recv or by being closed early.
	var done atomic.Bool

	finish := func(err error) {
		if onDone != nil && done.CompareAndSwap(false, true) {
			onDone(err)
		}
	}

	inner := s.recv

	s.recv = func() (*Chunk, error) {
		chunk, err := inner()
		if err != nil {
			finish(err)

			return chunk, err
		}

		if onChunk != nil && chunk != nil {
			onChunk(chunk)
		}

		return chunk, nil
	}

	return WrapStreamClose(s, func(*schema.Usage) { finish(nil) })
}

// StreamAccumulator rebuilds a complete assistant turn from the chunks of a
// stream. Vendors fragment a turn across many events — text arrives in pieces
// and each tool call's arguments are split across several deltas — so the
// accumulator merges fragments by tool-call index and hands back one finished
// message at the end.
type StreamAccumulator struct {
	text     []byte
	thinking []byte

	// calls collects tool calls by their stream index. Vendors interleave
	// fragments of parallel calls, so fragments are merged positionally.
	calls []accumulatedCall

	finishReason FinishReason
}

// accumulatedCall is one tool call being assembled from stream fragments.
type accumulatedCall struct {
	id   string
	name string
	args []byte
}

// Add merges one chunk into the accumulated turn.
func (a *StreamAccumulator) Add(chunk *Chunk) {
	if chunk == nil {
		return
	}

	a.text = append(a.text, chunk.TextDelta...)
	a.thinking = append(a.thinking, chunk.ThinkingDelta...)

	if chunk.FinishReason != "" {
		a.finishReason = chunk.FinishReason
	}

	for i := range chunk.ToolCallDeltas {
		a.addToolCallDelta(&chunk.ToolCallDeltas[i])
	}
}

// addToolCallDelta merges a tool-call fragment into the call at its index,
// growing the slice with placeholders when fragments arrive out of order.
func (a *StreamAccumulator) addToolCallDelta(d *ToolCallDelta) {
	idx := d.Index
	if idx < 0 {
		idx = 0
	}

	for idx >= len(a.calls) {
		a.calls = append(a.calls, accumulatedCall{})
	}

	call := &a.calls[idx]

	if d.ID != "" {
		call.id = d.ID
	}

	if d.Name != "" {
		call.name = d.Name
	}

	call.args = append(call.args, d.ArgumentsDelta...)
}

// Text returns the assistant text accumulated so far.
func (a *StreamAccumulator) Text() string { return string(a.text) }

// Thinking returns the reasoning text accumulated so far.
func (a *StreamAccumulator) Thinking() string { return string(a.thinking) }

// FinishReason returns the terminal finish reason, or empty if the stream has
// not reported one.
func (a *StreamAccumulator) FinishReason() FinishReason { return a.finishReason }

// AssistantMessage renders the accumulated turn as one assistant message in
// the wire form of proto, so a streamed turn can be appended to the
// conversation and replayed on the next iteration exactly like a
// non-streamed one.
func (a *StreamAccumulator) AssistantMessage(proto schema.Protocol) schema.Message {
	return schema.NewAssistantTurn(proto, a.Text(), a.Thinking(), a.ToolCalls())
}

// ToolCalls returns the fully merged tool calls, dropping any index that never
// received a name (a placeholder grown for an out-of-order fragment that no
// call ever filled).
func (a *StreamAccumulator) ToolCalls() []schema.ToolCall {
	var calls []schema.ToolCall

	for _, c := range a.calls {
		if c.name == "" {
			continue
		}

		args := string(c.args)
		if args == "" {
			args = "{}"
		}

		calls = append(calls, schema.ToolCall{ID: c.id, Name: c.name, Arguments: args})
	}

	return calls
}
