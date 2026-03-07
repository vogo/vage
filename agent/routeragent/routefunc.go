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

package routeragent

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/schema"
)

// FirstFunc always selects the first route.
func FirstFunc(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("routeragent: no routes available")
	}
	return &RouteResult{Agent: routes[0].Agent}, nil
}

// IndexFunc returns a RouteFunc that selects routes[i]. It returns an error if the
// index is out of range.
func IndexFunc(i int) RouteFunc {
	return func(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
		if i < 0 || i >= len(routes) {
			return nil, fmt.Errorf("routeragent: index %d out of range [0, %d)", i, len(routes))
		}
		return &RouteResult{Agent: routes[i].Agent}, nil
	}
}

// KeywordFunc returns a RouteFunc that scans the last user message for a
// case-insensitive substring match against each route's Description. It selects
// the first matching route. If no route matches, the fallback index is used
// when >= 0; otherwise an error is returned.
//
// Pass fallback < 0 to disable fallback behavior.
func KeywordFunc(fallback int) RouteFunc {
	return func(_ context.Context, req *schema.RunRequest, routes []Route) (*RouteResult, error) {
		if len(routes) == 0 {
			return nil, fmt.Errorf("routeragent: no routes available")
		}

		text := lastUserMessageText(req)
		lower := strings.ToLower(text)

		for _, r := range routes {
			if r.Description != "" && strings.Contains(lower, strings.ToLower(r.Description)) {
				return &RouteResult{Agent: r.Agent}, nil
			}
		}

		if fallback >= 0 && fallback < len(routes) {
			return &RouteResult{Agent: routes[fallback].Agent}, nil
		}

		return nil, fmt.Errorf("routeragent: no route matched keyword in %q", text)
	}
}

// RandomFunc randomly selects one route.
func RandomFunc(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("routeragent: no routes available")
	}
	return &RouteResult{Agent: routes[rand.Intn(len(routes))].Agent}, nil
}

// LLMFunc returns a RouteFunc that uses an LLM to select the best route based
// on route descriptions and the user's message. It sends a prompt listing all
// routes to the ChatCompleter and parses the returned index.
//
// If the LLM call fails or returns an unparseable/out-of-range response,
// the fallback index is used when >= 0; otherwise an error is returned.
// Pass fallback < 0 to disable fallback behavior.
func LLMFunc(cc aimodel.ChatCompleter, model string, fallback int) RouteFunc {
	return func(ctx context.Context, req *schema.RunRequest, routes []Route) (*RouteResult, error) {
		if len(routes) == 0 {
			return nil, fmt.Errorf("routeragent: no routes available")
		}

		var sb strings.Builder
		sb.WriteString("You are a routing agent. Based on the user's message, select the most appropriate agent to handle the request.\n\n")
		sb.WriteString("Available agents:\n")
		for i, r := range routes {
			fmt.Fprintf(&sb, "%d: %s\n", i, r.Description)
		}
		sb.WriteString("\nRespond with ONLY the index number of the selected agent. No other text.")

		userText := lastUserMessageText(req)

		chatReq := &aimodel.ChatRequest{
			Model: model,
			Messages: []aimodel.Message{
				{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent(sb.String())},
				{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(userText)},
			},
		}

		resp, err := cc.ChatCompletion(ctx, chatReq)
		if err != nil {
			if fallback >= 0 && fallback < len(routes) {
				return &RouteResult{Agent: routes[fallback].Agent}, nil
			}
			return nil, fmt.Errorf("routeragent: LLM routing: %w", err)
		}

		usage := &resp.Usage

		if len(resp.Choices) == 0 {
			if fallback >= 0 && fallback < len(routes) {
				return &RouteResult{Agent: routes[fallback].Agent, Usage: usage}, nil
			}
			return nil, fmt.Errorf("routeragent: LLM returned empty choices")
		}

		text := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
		idx, parseErr := strconv.Atoi(text)
		if parseErr != nil {
			if fallback >= 0 && fallback < len(routes) {
				return &RouteResult{Agent: routes[fallback].Agent, Usage: usage}, nil
			}
			return nil, fmt.Errorf("routeragent: LLM returned non-numeric response %q", text)
		}

		if idx < 0 || idx >= len(routes) {
			if fallback >= 0 && fallback < len(routes) {
				return &RouteResult{Agent: routes[fallback].Agent, Usage: usage}, nil
			}
			return nil, fmt.Errorf("routeragent: LLM returned index %d out of range [0, %d)", idx, len(routes))
		}

		return &RouteResult{Agent: routes[idx].Agent, Usage: usage}, nil
	}
}

// lastUserMessageText returns the text of the last message in the request.
func lastUserMessageText(req *schema.RunRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Content.Text()
}
