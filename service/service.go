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
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"sort"
	"sync"

	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/tool"
)

const (
	// DefaultMaxRequestSize is the default maximum request body size (4 MB).
	DefaultMaxRequestSize int64 = 4 << 20

	// DefaultMaxTasks is the default maximum number of async tasks.
	DefaultMaxTasks = 1000

	// DefaultTaskTTL is the default TTL for completed/failed tasks.
	DefaultTaskTTL = 0 // 0 means no automatic expiry

	// DefaultHeartbeatInterval is the default SSE heartbeat interval in seconds.
	DefaultHeartbeatInterval = 15
)

// Config holds the service configuration.
type Config struct {
	Addr string // Listen address, e.g. ":8080". Use ":0" for a random port.
}

// Option configures a Service.
type Option func(*Service)

// WithToolRegistry sets the tool registry used by the service.
func WithToolRegistry(r tool.ToolRegistry) Option {
	return func(s *Service) {
		s.tools = r
	}
}

// WithMaxRequestSize sets the maximum request body size in bytes.
func WithMaxRequestSize(n int64) Option {
	return func(s *Service) {
		s.maxRequestSize = n
	}
}

// WithMaxTasks sets the maximum number of async tasks.
func WithMaxTasks(n int) Option {
	return func(s *Service) {
		s.tasks.maxTasks = n
	}
}

// WithHeartbeatInterval sets the SSE heartbeat interval in seconds.
func WithHeartbeatInterval(seconds int) Option {
	return func(s *Service) {
		s.heartbeatSec = seconds
	}
}

// ErrorResponse is the standard error response body.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Service manages agent execution over HTTP.
type Service struct {
	cfg            Config
	mu             sync.RWMutex
	agents         map[string]agent.Agent
	agentOrder     []string // insertion-order agent IDs
	tools          tool.ToolRegistry
	tasks          *TaskStore
	server         *http.Server
	ln             net.Listener
	maxRequestSize int64
	heartbeatSec   int
}

// New creates a new Service with the given configuration and options.
func New(cfg Config, opts ...Option) *Service {
	s := &Service{
		cfg:            cfg,
		agents:         make(map[string]agent.Agent),
		tasks:          NewTaskStore(DefaultMaxTasks),
		maxRequestSize: DefaultMaxRequestSize,
		heartbeatSec:   DefaultHeartbeatInterval,
	}

	for _, o := range opts {
		o(s)
	}

	return s
}

// RegisterAgent adds an agent to the service.
func (s *Service) RegisterAgent(a agent.Agent) {
	s.mu.Lock()
	id := a.ID()
	if _, exists := s.agents[id]; !exists {
		s.agentOrder = append(s.agentOrder, id)
	}
	s.agents[id] = a
	s.mu.Unlock()
}

// getAgent returns the agent with the given ID, if it exists.
func (s *Service) getAgent(id string) (agent.Agent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	return a, ok
}

// listAgentsSorted returns agents sorted by ID.
func (s *Service) listAgentsSorted() []agent.Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]agent.Agent, 0, len(s.agents))
	for _, a := range s.agents {
		result = append(result, a)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})

	return result
}

// Handler returns the HTTP handler for the service.
// This is useful for testing with httptest.
func (s *Service) Handler() http.Handler {
	return s.buildMux()
}

// Start begins listening and serving HTTP requests.
// It blocks until the context is canceled or an error occurs.
func (s *Service) Start(ctx context.Context) error {
	mux := s.buildMux()

	s.server = &http.Server{
		Handler: mux,
	}

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	// Shut down when context is canceled.
	go func() {
		<-ctx.Done()
		_ = s.Shutdown(context.Background())
	}()

	if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Service) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// ListenAddr returns the actual address the server is listening on.
// Returns an empty string if the server has not started.
func (s *Service) ListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return ""
}

// requestIDMiddleware injects a unique X-Request-ID header into each request and response.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}

		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

// buildMux creates the HTTP mux with all routes registered.
func (s *Service) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /v1/health", requestIDMiddleware(http.HandlerFunc(s.handleHealth)))
	mux.Handle("GET /v1/agents", requestIDMiddleware(http.HandlerFunc(s.handleListAgents)))
	mux.Handle("GET /v1/agents/{id}", requestIDMiddleware(http.HandlerFunc(s.handleGetAgent)))
	mux.Handle("POST /v1/agents/{id}/run", requestIDMiddleware(http.HandlerFunc(s.handleRun)))
	mux.Handle("POST /v1/agents/{id}/stream", requestIDMiddleware(http.HandlerFunc(s.handleStream)))
	mux.Handle("POST /v1/agents/{id}/async", requestIDMiddleware(http.HandlerFunc(s.handleAsync)))
	mux.Handle("GET /v1/tools", requestIDMiddleware(http.HandlerFunc(s.handleListTools)))
	mux.Handle("GET /v1/tasks/{taskID}", requestIDMiddleware(http.HandlerFunc(s.handleGetTask)))
	mux.Handle("POST /v1/tasks/{taskID}/cancel", requestIDMiddleware(http.HandlerFunc(s.handleCancelTask)))

	return mux
}
