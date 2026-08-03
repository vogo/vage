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
	"context"
	"errors"
	"io"
	"sync"

	"github.com/vogo/vage/schema"
)

// ErrFakeExhausted reports that a FakeCaller ran out of scripted responses.
var ErrFakeExhausted = errors.New("vage: fake caller has no more responses")

// FakeCaller is a scripted Caller for tests. It replays a fixed list of
// responses in order, records every request it received, and can be pinned to
// either protocol so a test can assert that behaviour holds for both wire
// forms.
//
// It lives in the production package (rather than a _test file) because
// tests across many packages drive agents through this seam.
//
// A FakeCaller is safe for concurrent use.
type FakeCaller struct {
	mu sync.Mutex

	// Proto is the protocol this caller reports. The zero value means
	// ProtocolOpenAIChat.
	Proto schema.Protocol

	// Responses are replayed in order, one per Call. When they run out,
	// Call returns ErrFakeExhausted.
	Responses []*Response

	// Chunks, when set, is the stream CallStream replays.
	Chunks []*Chunk

	// Err, when set, is returned by every Call and CallStream instead of a
	// scripted result.
	Err error

	calls    int
	requests []*Request
}

// Protocol implements Caller.
func (f *FakeCaller) Protocol() schema.Protocol {
	if f.Proto == "" {
		return schema.ProtocolOpenAIChat
	}

	return f.Proto
}

// Call implements Caller, replaying the next scripted response.
func (f *FakeCaller) Call(_ context.Context, req *Request) (*Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, req)

	if f.Err != nil {
		return nil, f.Err
	}

	if f.calls >= len(f.Responses) {
		return nil, ErrFakeExhausted
	}

	resp := f.Responses[f.calls]
	f.calls++

	return resp, nil
}

// CallStream implements Caller, replaying Chunks as a stream.
func (f *FakeCaller) CallStream(_ context.Context, req *Request) (*Stream, error) {
	f.mu.Lock()

	f.requests = append(f.requests, req)

	if f.Err != nil {
		err := f.Err
		f.mu.Unlock()

		return nil, err
	}

	chunks := f.Chunks
	f.mu.Unlock()

	var i int

	return NewStream(func() (*Chunk, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}

		chunk := chunks[i]
		i++

		return chunk, nil
	}, func() error { return nil }), nil
}

// Calls reports how many times Call replayed a response.
func (f *FakeCaller) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// Requests returns every request the caller received, in order.
func (f *FakeCaller) Requests() []*Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*Request, len(f.requests))
	copy(out, f.requests)

	return out
}

// FakeStopResponse builds a scripted response for a turn that ends normally.
func FakeStopResponse(proto schema.Protocol, text string, usage schema.Usage) *Response {
	return &Response{
		Message:      schema.NewAssistantTurn(proto, text, "", nil),
		FinishReason: FinishReasonStop,
		Usage:        usage,
	}
}

// FakeToolCallResponse builds a scripted response for a turn that requests
// tool calls.
func FakeToolCallResponse(proto schema.Protocol, calls []schema.ToolCall, usage schema.Usage) *Response {
	return &Response{
		Message:      schema.NewAssistantTurn(proto, "", "", calls),
		FinishReason: FinishReasonToolCalls,
		Usage:        usage,
	}
}

var _ Caller = (*FakeCaller)(nil)
