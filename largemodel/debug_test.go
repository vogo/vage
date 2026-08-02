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
	"sync"
	"testing"

	"github.com/vogo/vage/schema"
)

type captureSink struct {
	mu      sync.Mutex
	records []capRec
	counter int
}

type capRec struct {
	Kind   string
	Corr   string
	Fields map[string]any
}

func (s *captureSink) Emit(_ context.Context, kind, corr string, fields map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, capRec{Kind: kind, Corr: corr, Fields: fields})
}

func (s *captureSink) NewCorrelationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return "corr-1"
}

type stubCompleter struct {
	resp   *Response
	err    error
	stream *Stream
}

func (s *stubCompleter) Protocol() schema.Protocol { return schema.ProtocolOpenAIChat }

func (s *stubCompleter) Call(_ context.Context, _ *Request) (*Response, error) {
	return s.resp, s.err
}

func (s *stubCompleter) CallStream(_ context.Context, _ *Request) (*Stream, error) {
	return s.stream, s.err
}

func TestDebugMiddleware_Capture(t *testing.T) {
	sink := &captureSink{}
	mw := NewDebugMiddleware(sink)
	stub := &stubCompleter{
		resp: &Response{
			Model:        "m",
			Message:      schema.NewAssistantTurn(schema.ProtocolOpenAIChat, "hi", "", nil),
			FinishReason: "stop",
			Usage:        schema.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		},
	}
	c := mw.Wrap(stub)

	_, err := c.Call(context.Background(), &Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	if len(sink.records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(sink.records))
	}
	if sink.records[0].Kind != KindLLMRequest || sink.records[1].Kind != KindLLMResponse {
		t.Errorf("wrong kinds: %v", sink.records)
	}
	if sink.records[0].Corr != sink.records[1].Corr {
		t.Errorf("corr mismatch")
	}
	if got := sink.records[1].Fields["content"]; got != "hi" {
		t.Errorf("expected content 'hi', got %v", got)
	}
}

func TestDebugMiddleware_Error(t *testing.T) {
	sink := &captureSink{}
	mw := NewDebugMiddleware(sink)
	stub := &stubCompleter{err: errors.New("bad")}
	c := mw.Wrap(stub)

	_, err := c.Call(context.Background(), &Request{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(sink.records) != 2 {
		t.Fatalf("expected request+error records, got %d", len(sink.records))
	}
	if sink.records[1].Kind != KindLLMError {
		t.Errorf("expected error kind, got %s", sink.records[1].Kind)
	}
}

func TestDebugMiddleware_NoopSink(t *testing.T) {
	mw := NewDebugMiddleware(nil)
	stub := &stubCompleter{resp: &Response{}}
	c := mw.Wrap(stub)
	if _, err := c.Call(context.Background(), &Request{}); err != nil {
		t.Fatal(err)
	}
}

func TestNewCorrelationID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := NewCorrelationID()
		if id == "" {
			t.Fatal("empty id")
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
