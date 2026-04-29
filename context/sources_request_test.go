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

package vctx

import (
	"context"
	"testing"

	"github.com/vogo/vage/schema"
)

// TestRequestMessagesSource_Pass verifies request messages flow through.
func TestRequestMessagesSource_Pass(t *testing.T) {
	src := &RequestMessagesSource{}
	req := &schema.RunRequest{
		Messages: []schema.Message{
			schema.NewUserMessage("hi"),
			schema.NewUserMessage("again"),
		},
	}

	res, err := src.Fetch(context.Background(), FetchInput{Request: req})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(res.Messages))
	}
	if !src.MustInclude() {
		t.Errorf("MustInclude = false, want true")
	}
	if res.Report.Status != StatusOK {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusOK)
	}
}

// TestRequestMessagesSource_NilRequest verifies a nil request produces
// a skip rather than panic.
func TestRequestMessagesSource_NilRequest(t *testing.T) {
	src := &RequestMessagesSource{}
	res, err := src.Fetch(context.Background(), FetchInput{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}

// TestRequestMessagesSource_EmptyRequest verifies an empty Messages slice
// produces a skip.
func TestRequestMessagesSource_EmptyRequest(t *testing.T) {
	src := &RequestMessagesSource{}
	req := &schema.RunRequest{}
	res, err := src.Fetch(context.Background(), FetchInput{Request: req})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.Report.Status != StatusSkipped {
		t.Errorf("Status = %q, want %q", res.Report.Status, StatusSkipped)
	}
}
