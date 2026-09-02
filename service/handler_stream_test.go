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

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
)

// floodAgent streams events as fast as the channel accepts them, so a client
// that stops reading fills the handler's buffer instead of racing it empty.
type floodAgent struct {
	agent.Agent

	stopped chan struct{}
}

func (f floodAgent) RunStream(context.Context, *schema.RunRequest) (*schema.RunStream, error) {
	// Deliberately detached from the handler's context. The question under
	// test is whether the handler releases its forwarding goroutine — which is
	// what closes the stream. Honouring the request context here would stop
	// this producer on client disconnect regardless, masking a forwarder that
	// is still parked on a full channel.
	return schema.NewRunStream(context.Background(), 8, func(_ context.Context, send func(schema.Event) error) error {
		defer close(f.stopped)

		for {
			e := schema.NewEvent(schema.EventTextDelta, "flood", "s", schema.TextDeltaData{Delta: "x"})
			if err := send(e); err != nil {
				return err
			}
		}
	}), nil
}

// TestHandleStream_ClientGoneReleasesForwarder pins that a client walking away
// mid-stream does not strand the forwarding goroutine on a full buffer. The
// forwarder writes into a 64-item channel that only the writer loop drains, so
// an unguarded send parks forever once that loop returns — and the run stream
// stays open with it, because ForEach's deferred Close never runs.
func TestHandleStream_ClientGoneReleasesForwarder(t *testing.T) {
	base := agent.NewCustomAgent(agent.Config{ID: "flood", Name: "Flood"},
		func(context.Context, *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{}, nil
		})

	stopped := make(chan struct{})

	svc := New(Config{Addr: ":0"})
	svc.RegisterAgent(floodAgent{Agent: base, stopped: stopped})

	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/agents/flood/stream", "application/json",
		strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// Read one chunk to prove the stream is live, then walk away without
	// draining the rest so the handler's buffer fills up behind us.
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}

	_ = resp.Body.Close()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("producer still running: the forwarding goroutine never released the stream")
	}
}
