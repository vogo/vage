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
	"io"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// TestStream_CloseUnblocksInFlightRecv is the regression test for the lifecycle
// guarantee every streaming consumer relies on: a Recv blocked on a network
// read is unblocked by closing the stream from another goroutine.
//
// The subtlety is that Recv holds the stream mutex while it blocks, so nothing
// on the Close path may take that mutex — including the usage snapshot handed
// to the onClose callback, which every accounting middleware installs. Reading
// usage under the mutex deadlocks Close against the very Recv it is meant to
// release, and does so under the default configuration.
func TestStream_CloseUnblocksInFlightRecv(t *testing.T) {
	// blocked releases the fake network read; release closes it, exactly as
	// closing a response body would unblock a real one.
	blocked := make(chan struct{})
	entered := make(chan struct{})

	s := NewStream(
		func() (*Chunk, error) {
			close(entered)
			<-blocked

			return nil, io.EOF
		},
		func() error {
			close(blocked)

			return nil
		},
	)

	// An accounting middleware makes onClose non-nil, which is what puts the
	// usage read on the Close path.
	var recorded *schema.Usage

	WrapStreamClose(s, func(u *schema.Usage) { recorded = u })

	recvDone := make(chan struct{})

	go func() {
		defer close(recvDone)

		if _, err := s.Recv(); err != io.EOF {
			t.Errorf("Recv() error = %v, want io.EOF", err)
		}
	}()

	<-entered

	closeDone := make(chan struct{})

	go func() {
		defer close(closeDone)

		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() deadlocked against an in-flight Recv")
	}

	select {
	case <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Recv() was not released by Close()")
	}

	// The callback fires exactly once even though nothing was ever streamed.
	if recorded != nil {
		t.Errorf("onClose usage = %+v, want nil for a stream that reported none", recorded)
	}
}

// TestStream_CloseReportsCapturedUsage covers the other half of the same
// contract: the usage a stream saw in flight must reach onClose, or a consumer
// that abandons a stream mid-way is never billed for it.
func TestStream_CloseReportsCapturedUsage(t *testing.T) {
	chunks := []*Chunk{
		{TextDelta: "hi"},
		{Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}

	i := 0
	s := NewStream(
		func() (*Chunk, error) {
			if i >= len(chunks) {
				return nil, io.EOF
			}

			c := chunks[i]
			i++

			return c, nil
		},
		func() error { return nil },
	)

	var recorded *schema.Usage

	WrapStreamClose(s, func(u *schema.Usage) { recorded = u })

	for {
		if _, err := s.Recv(); err != nil {
			break
		}
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if recorded == nil {
		t.Fatal("onClose usage = nil, want the usage the stream reported")
	}

	if recorded.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", recorded.TotalTokens)
	}
}

// TestStream_CloseIsIdempotent pins that the release and the accounting
// callback each run exactly once, however many times Close is called — a
// consumer that closes in a defer as well as on its error path must not be
// billed twice.
func TestStream_CloseIsIdempotent(t *testing.T) {
	releases := 0
	closes := 0

	s := NewStream(
		func() (*Chunk, error) { return nil, io.EOF },
		func() error {
			releases++

			return nil
		},
	)

	WrapStreamClose(s, func(*schema.Usage) { closes++ })

	for range 3 {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	if releases != 1 {
		t.Errorf("release ran %d times, want 1", releases)
	}

	if closes != 1 {
		t.Errorf("onClose ran %d times, want 1", closes)
	}

	if _, err := s.Recv(); err != ErrStreamClosed {
		t.Errorf("Recv after Close = %v, want ErrStreamClosed", err)
	}
}
