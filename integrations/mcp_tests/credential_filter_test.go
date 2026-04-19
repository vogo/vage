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

package mcp_tests

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcpclient "github.com/vogo/vage/mcp/client"
	mcpserver "github.com/vogo/vage/mcp/server"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/security/credscrub"
)

// credFilterFixture hosts a client + server connected via in-memory
// transports. The handler argument captures what the server actually
// received so tests can assert redaction or non-invocation.
type credFilterFixture struct {
	t         *testing.T
	cli       *mcpclient.Client
	srv       *mcpserver.Server
	cancel    context.CancelFunc
	handlerFn func(ctx context.Context, args map[string]any) (schema.ToolResult, error)
	invoked   *atomic.Bool
}

// newCredFilterFixture wires up an in-memory MCP pair. The caller-provided
// handler is invoked inside the server's "echo" tool. Use the fixture's
// Close() to tear everything down.
func newCredFilterFixture(
	t *testing.T,
	clientOpts []mcpclient.Option,
	serverOpts []mcpserver.Option,
	handler func(ctx context.Context, args map[string]any) (schema.ToolResult, error),
) *credFilterFixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	srv := mcpserver.NewServer(serverOpts...)
	invoked := &atomic.Bool{}

	wrapped := func(c context.Context, args map[string]any) (schema.ToolResult, error) {
		invoked.Store(true)
		return handler(c, args)
	}

	if err := srv.RegisterTool(mcpserver.ToolRegistration{
		Name:        "echo",
		Description: "Echo tool that mirrors args and runs the test handler",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text":          map[string]any{"type": "string"},
				"authorization": map[string]any{"type": "string"},
				"nested":        map[string]any{"type": "object"},
			},
		},
		Handler: wrapped,
	}); err != nil {
		cancel()
		t.Fatalf("RegisterTool: %v", err)
	}

	// Serve in background. Ignore error on shutdown.
	go func() {
		_ = srv.Serve(ctx, serverTransport)
	}()

	cli := mcpclient.NewClient("test://credfilter", clientOpts...)
	if err := cli.Connect(ctx, clientTransport); err != nil {
		cancel()
		t.Fatalf("Connect: %v", err)
	}

	return &credFilterFixture{
		t:         t,
		cli:       cli,
		srv:       srv,
		cancel:    cancel,
		handlerFn: wrapped,
		invoked:   invoked,
	}
}

// Close tears down the client and cancels the server context.
func (f *credFilterFixture) Close() {
	if err := f.cli.Disconnect(); err != nil {
		f.t.Logf("Disconnect: %v", err)
	}
	f.cancel()
}

// realistic credential fixtures used throughout. These look real but are
// not actual credentials — they are documented test vectors.
const (
	awsKey          = "AKIAIOSFODNN7EXAMPLE"
	githubToken     = "ghp_1234567890abcdefghij1234567890abcdefgh"
	secondAWSKey    = "AKIAABCDEFGHIJKLMNOP" // different plaintext, same type
	googleAPIKeyStr = "AIzaSyA1234567890123456789012345678901234"
)

// collectingCallback captures scan events for later assertion. Safe for
// concurrent use.
type collectingCallback struct {
	mu     sync.Mutex
	events []mcpclient.ScanEvent
}

func (c *collectingCallback) fn(_ context.Context, ev mcpclient.ScanEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collectingCallback) snapshot() []mcpclient.ScanEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]mcpclient.ScanEvent, len(c.events))
	copy(out, c.events)
	return out
}

type collectingServerCallback struct {
	mu     sync.Mutex
	events []mcpserver.ScanEvent
}

func (c *collectingServerCallback) fn(_ context.Context, ev mcpserver.ScanEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collectingServerCallback) snapshot() []mcpserver.ScanEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]mcpserver.ScanEvent, len(c.events))
	copy(out, c.events)
	return out
}

// TestCredentialFilter_ClientOutboundRedact verifies that when a client
// scanner is configured with ActionRedact and outbound args contain a
// credential, the server handler sees the redacted value (plaintext is
// never transmitted).
func TestCredentialFilter_ClientOutboundRedact(t *testing.T) {
	ctx := context.Background()

	var receivedAuth string
	handler := func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
		if v, ok := args["authorization"].(string); ok {
			receivedAuth = v
		}
		return schema.TextResult("", "ok"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	cb := &collectingCallback{}
	f := newCredFilterFixture(t,
		[]mcpclient.Option{
			mcpclient.WithCredentialScanner(scanner),
			mcpclient.WithScanCallback(cb.fn),
		},
		nil, handler)
	defer f.Close()

	args := `{"authorization":"Bearer ` + awsKey + `","text":"` + awsKey + `"}`
	res, err := f.cli.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError=true; text=%q", resultText(res))
	}

	if strings.Contains(receivedAuth, awsKey) {
		t.Errorf("plaintext credential leaked to server handler: %q", receivedAuth)
	}
	if !strings.Contains(receivedAuth, "[REDACTED:") {
		t.Errorf("expected redaction marker in server-received authorization, got %q", receivedAuth)
	}

	evs := cb.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected at least one scan event; got none")
	}
	sawOutbound := false
	for _, ev := range evs {
		if ev.Direction == mcpclient.DirectionOutbound {
			sawOutbound = true
		}
	}
	if !sawOutbound {
		t.Errorf("expected mcp_outbound scan event; got %+v", evs)
	}
}

// TestCredentialFilter_ClientOutboundBlock verifies that ActionBlock on
// the client side returns an error result without invoking the server
// handler at all.
func TestCredentialFilter_ClientOutboundBlock(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "server should not see this"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})

	f := newCredFilterFixture(t,
		[]mcpclient.Option{mcpclient.WithCredentialScanner(scanner)},
		nil, handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	res, err := f.cli.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for blocked call; text=%q", resultText(res))
	}
	if !strings.Contains(resultText(res), "blocked by mcp credential filter") {
		t.Errorf("expected block message, got %q", resultText(res))
	}
	if f.invoked.Load() {
		t.Errorf("server handler was invoked despite ActionBlock")
	}
}

// TestCredentialFilter_ClientOutboundLog verifies that ActionLog lets the
// request pass through unchanged while still firing the scan callback.
func TestCredentialFilter_ClientOutboundLog(t *testing.T) {
	ctx := context.Background()

	var receivedAuth string
	handler := func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
		if v, ok := args["authorization"].(string); ok {
			receivedAuth = v
		}
		return schema.TextResult("", "ok"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionLog})

	cb := &collectingCallback{}
	f := newCredFilterFixture(t,
		[]mcpclient.Option{
			mcpclient.WithCredentialScanner(scanner),
			mcpclient.WithScanCallback(cb.fn),
		},
		nil, handler)
	defer f.Close()

	authValue := "Bearer " + awsKey + "plus-extra-padding-chars"
	args := `{"authorization":"` + authValue + `"}`
	if _, err := f.cli.CallTool(ctx, "echo", args); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	// NOTE: the "authorization" key is a FieldRule, so even under ActionLog
	// the server still receives the original value (log does not mutate).
	if receivedAuth != authValue {
		t.Errorf("expected args to pass through unchanged under ActionLog; got %q, want %q", receivedAuth, authValue)
	}

	evs := cb.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected scan callback to fire under ActionLog; got none")
	}
	for _, ev := range evs {
		if len(ev.Hits) == 0 {
			t.Errorf("expected hits in callback event; got empty")
		}
		if ev.Action != credscrub.ActionLog {
			t.Errorf("ev.Action = %q, want %q", ev.Action, credscrub.ActionLog)
		}
	}
}

// TestCredentialFilter_ClientInboundRedact verifies that client-side
// ActionRedact rewrites credentials in the returned text, never exposing
// the plaintext to the caller.
func TestCredentialFilter_ClientInboundRedact(t *testing.T) {
	ctx := context.Background()

	// Handler returns credential text in the result.
	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "here is your key: "+awsKey+" done"), nil
	}

	// Server has no scanner; client scanner will handle inbound.
	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	f := newCredFilterFixture(t,
		[]mcpclient.Option{mcpclient.WithCredentialScanner(scanner)},
		nil, handler)
	defer f.Close()

	res, err := f.cli.CallTool(ctx, "echo", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError=true")
	}
	txt := resultText(res)
	if strings.Contains(txt, awsKey) {
		t.Errorf("plaintext credential leaked through to client: %q", txt)
	}
	if !strings.Contains(txt, "[REDACTED:aws_access_key]") {
		t.Errorf("expected [REDACTED:aws_access_key] in client result, got %q", txt)
	}
}

// TestCredentialFilter_ClientInboundBlock verifies that client-side
// ActionBlock turns a credential-laden result into an error result.
func TestCredentialFilter_ClientInboundBlock(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "secret: "+githubToken), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})

	f := newCredFilterFixture(t,
		[]mcpclient.Option{mcpclient.WithCredentialScanner(scanner)},
		nil, handler)
	defer f.Close()

	res, err := f.cli.CallTool(ctx, "echo", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true; got %q", resultText(res))
	}
	if !strings.Contains(resultText(res), "inbound") {
		t.Errorf("expected inbound-block message, got %q", resultText(res))
	}
	if strings.Contains(resultText(res), githubToken) {
		t.Errorf("plaintext credential leaked in block message: %q", resultText(res))
	}
}

// TestCredentialFilter_ServerInboundRedact verifies that a server-side
// scanner with ActionRedact rewrites the inbound args before the handler
// sees them.
func TestCredentialFilter_ServerInboundRedact(t *testing.T) {
	ctx := context.Background()

	var receivedToken string
	handler := func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
		if v, ok := args["token"].(string); ok {
			receivedToken = v
		}
		return schema.TextResult("", "ok"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	f := newCredFilterFixture(t, nil,
		[]mcpserver.Option{mcpserver.WithCredentialScanner(scanner)},
		handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	if _, err := f.cli.CallTool(ctx, "echo", args); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if strings.Contains(receivedToken, githubToken) {
		t.Errorf("plaintext leaked to handler: %q", receivedToken)
	}
	if !strings.Contains(receivedToken, "[REDACTED:") {
		t.Errorf("expected redaction marker, got %q", receivedToken)
	}
}

// TestCredentialFilter_ServerInboundBlock verifies that a server-side
// scanner with ActionBlock prevents the handler from running and returns
// an error result to the client.
func TestCredentialFilter_ServerInboundBlock(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "should not reach here"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})

	f := newCredFilterFixture(t, nil,
		[]mcpserver.Option{mcpserver.WithCredentialScanner(scanner)},
		handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	res, err := f.cli.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true; got %q", resultText(res))
	}
	if f.invoked.Load() {
		t.Errorf("server handler was invoked despite server-side ActionBlock")
	}
	if !strings.Contains(resultText(res), "server-in") {
		t.Errorf("expected server-in block message, got %q", resultText(res))
	}
}

// TestCredentialFilter_ServerOutboundRedact verifies that when the server
// has ActionRedact and the handler returns credential text, the client
// sees the redacted form.
func TestCredentialFilter_ServerOutboundRedact(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "your key is "+awsKey+" (keep it safe)"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	f := newCredFilterFixture(t, nil,
		[]mcpserver.Option{mcpserver.WithCredentialScanner(scanner)},
		handler)
	defer f.Close()

	res, err := f.cli.CallTool(ctx, "echo", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError=true; text=%q", resultText(res))
	}
	txt := resultText(res)
	if strings.Contains(txt, awsKey) {
		t.Errorf("plaintext credential leaked through server outbound redact: %q", txt)
	}
	if !strings.Contains(txt, "[REDACTED:aws_access_key]") {
		t.Errorf("expected redaction marker, got %q", txt)
	}
}

// TestCredentialFilter_ServerOutboundBlock verifies that when the server
// has ActionBlock and the handler returns credential text, the client
// receives an error result.
func TestCredentialFilter_ServerOutboundBlock(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "leaked: "+awsKey), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionBlock})

	f := newCredFilterFixture(t, nil,
		[]mcpserver.Option{mcpserver.WithCredentialScanner(scanner)},
		handler)
	defer f.Close()

	res, err := f.cli.CallTool(ctx, "echo", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true; got %q", resultText(res))
	}
	if !strings.Contains(resultText(res), "server-out") {
		t.Errorf("expected server-out block message, got %q", resultText(res))
	}
	if strings.Contains(resultText(res), awsKey) {
		t.Errorf("plaintext credential leaked in block message: %q", resultText(res))
	}
}

// TestCredentialFilter_ClientCombinedOutboundInbound verifies that when
// BOTH outbound args and inbound result carry (different) credentials,
// both get redacted and the callback fires once per direction.
func TestCredentialFilter_ClientCombinedOutboundInbound(t *testing.T) {
	ctx := context.Background()

	var receivedToken string
	handler := func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
		if v, ok := args["token"].(string); ok {
			receivedToken = v
		}
		// Respond with a different credential.
		return schema.TextResult("", "response with "+secondAWSKey+" inside"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	cb := &collectingCallback{}
	f := newCredFilterFixture(t,
		[]mcpclient.Option{
			mcpclient.WithCredentialScanner(scanner),
			mcpclient.WithScanCallback(cb.fn),
		},
		nil, handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	res, err := f.cli.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	// Outbound: server handler should not see the plaintext token.
	if strings.Contains(receivedToken, githubToken) {
		t.Errorf("plaintext outbound token leaked: %q", receivedToken)
	}

	// Inbound: client result should not contain plaintext second credential.
	txt := resultText(res)
	if strings.Contains(txt, secondAWSKey) {
		t.Errorf("plaintext inbound credential leaked: %q", txt)
	}
	if !strings.Contains(txt, "[REDACTED:aws_access_key]") {
		t.Errorf("expected inbound redaction marker, got %q", txt)
	}

	// Callback should have fired at least twice: once per direction.
	evs := cb.snapshot()
	var outbound, inbound int
	for _, ev := range evs {
		switch ev.Direction {
		case mcpclient.DirectionOutbound:
			outbound++
		case mcpclient.DirectionInbound:
			inbound++
		}
	}
	if outbound == 0 {
		t.Errorf("expected at least 1 outbound event; got 0 (events=%+v)", evs)
	}
	if inbound == 0 {
		t.Errorf("expected at least 1 inbound event; got 0 (events=%+v)", evs)
	}
}

// TestCredentialFilter_CallbackMaskedOnly verifies that the scan callback
// never receives plaintext credentials — only "first-4-chars + ****"
// masked previews.
func TestCredentialFilter_CallbackMaskedOnly(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "inbound leak: "+googleAPIKeyStr), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	cb := &collectingCallback{}
	f := newCredFilterFixture(t,
		[]mcpclient.Option{
			mcpclient.WithCredentialScanner(scanner),
			mcpclient.WithScanCallback(cb.fn),
		},
		nil, handler)
	defer f.Close()

	// Include a credential in args AND let the handler respond with another.
	args := `{"authorization":"Bearer ` + awsKey + `extra-bytes-to-satisfy-minlength"}`
	if _, err := f.cli.CallTool(ctx, "echo", args); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	evs := cb.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected at least one scan event")
	}

	fullCreds := []string{awsKey, googleAPIKeyStr}
	for i, ev := range evs {
		if len(ev.Hits) == 0 {
			t.Errorf("event %d has no hits", i)
			continue
		}
		for j, h := range ev.Hits {
			for _, full := range fullCreds {
				if strings.Contains(h.Masked, full) {
					t.Errorf("event %d hit %d Masked=%q contains full plaintext %q",
						i, j, h.Masked, full)
				}
			}
			// Masked value should conform to the maskCredential shape:
			// either "****" or "<4chars>****".
			if h.Masked != "****" && !strings.HasSuffix(h.Masked, "****") {
				t.Errorf("event %d hit %d Masked=%q does not look masked", i, j, h.Masked)
			}
		}
	}
}

// TestCredentialFilter_TruncatedPropagation verifies that when MaxScanBytes
// is smaller than the input and a credential sits past the cap, the
// Truncated flag propagates to the callback and the credential beyond the
// cap is not reported as a hit.
func TestCredentialFilter_TruncatedPropagation(t *testing.T) {
	ctx := context.Background()

	// Build a response where the credential is strictly past the first 32
	// bytes. The leading padding is benign text with no credentials.
	padding := strings.Repeat("x", 64)
	responseText := padding + " " + awsKey
	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", responseText), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{
		Action:       credscrub.ActionLog,
		MaxScanBytes: 32,
	})

	cb := &collectingCallback{}
	f := newCredFilterFixture(t,
		[]mcpclient.Option{
			mcpclient.WithCredentialScanner(scanner),
			mcpclient.WithScanCallback(cb.fn),
		},
		nil, handler)
	defer f.Close()

	if _, err := f.cli.CallTool(ctx, "echo", `{}`); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	evs := cb.snapshot()
	// With truncation at 32 bytes, the awsKey (past that) must not appear in hits.
	for _, ev := range evs {
		if ev.Direction != mcpclient.DirectionInbound {
			continue
		}
		if !ev.Truncated {
			t.Errorf("expected Truncated=true on inbound scan over MaxScanBytes, got false")
		}
		for _, h := range ev.Hits {
			if h.Type == "aws_access_key" {
				t.Errorf("unexpected hit past MaxScanBytes cap: %+v", h)
			}
		}
	}
}

// TestCredentialFilter_DefaultPassthrough verifies that a client/server
// built WITHOUT a scanner behaves exactly like before: args forwarded
// unchanged, result returned unchanged, no blocking.
func TestCredentialFilter_DefaultPassthrough(t *testing.T) {
	ctx := context.Background()

	var receivedToken string
	handler := func(_ context.Context, args map[string]any) (schema.ToolResult, error) {
		if v, ok := args["token"].(string); ok {
			receivedToken = v
		}
		return schema.TextResult("", "passthrough: "+awsKey), nil
	}

	f := newCredFilterFixture(t, nil, nil, handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	res, err := f.cli.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError=true with no scanner: %q", resultText(res))
	}
	if receivedToken != githubToken {
		t.Errorf("expected token to pass through unchanged; got %q, want %q", receivedToken, githubToken)
	}
	txt := resultText(res)
	if !strings.Contains(txt, awsKey) {
		t.Errorf("expected unchanged passthrough of result text; got %q", txt)
	}
	if strings.Contains(txt, "[REDACTED:") {
		t.Errorf("unexpected redaction in passthrough mode: %q", txt)
	}
}

// TestCredentialFilter_ServerCallbackReceivesEvents is a bonus test that
// asserts server-side callbacks also fire and receive masked hits, giving
// symmetric observability to the client-side tests.
func TestCredentialFilter_ServerCallbackReceivesEvents(t *testing.T) {
	ctx := context.Background()

	handler := func(_ context.Context, _ map[string]any) (schema.ToolResult, error) {
		return schema.TextResult("", "ok"), nil
	}

	scanner := credscrub.NewScanner(credscrub.Config{Action: credscrub.ActionRedact})

	cb := &collectingServerCallback{}
	f := newCredFilterFixture(t, nil,
		[]mcpserver.Option{
			mcpserver.WithCredentialScanner(scanner),
			mcpserver.WithScanCallback(cb.fn),
		},
		handler)
	defer f.Close()

	args := `{"token":"` + githubToken + `"}`
	if _, err := f.cli.CallTool(ctx, "echo", args); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	evs := cb.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected at least one server-side scan event")
	}
	for _, ev := range evs {
		for _, h := range ev.Hits {
			if strings.Contains(h.Masked, githubToken) {
				t.Errorf("server callback leaked plaintext token: %q", h.Masked)
			}
		}
	}
}

// resultText joins all text parts in a schema.ToolResult for assertion.
func resultText(r schema.ToolResult) string {
	var b strings.Builder
	for _, p := range r.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
