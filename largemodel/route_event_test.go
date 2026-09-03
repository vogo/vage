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
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/vage/largemodel/provider/openais"
	"github.com/vogo/vage/schema"
)

func TestComposeCaller_RouteSelectedEventHasNoCredentials(t *testing.T) {
	good := newCountingServer(t, 200, openAITextReply, 0)

	var mu sync.Mutex
	var events []schema.Event
	ctx := schema.WithEventDispatcher(context.Background(), func(_ context.Context, e schema.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	caller := mustOpenAIComposeCaller(t, StrategyFailover, []openais.EndpointSpec{
		{
			Alias:   "primary",
			BaseURL: good.URL,
			APIKey:  "sk-super-secret-credential-value",
			Model:   "gpt-test",
			Tags:    map[string]string{"role": "internal-tag"},
		},
	}, fastRouting())

	if _, err := caller.Call(ctx, simpleRequest(schema.ProtocolOpenAIChat)); err != nil {
		t.Fatalf("Call: %v", err)
	}

	mu.Lock()
	got := append([]schema.Event(nil), events...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("expected EventRouteSelected")
	}
	for _, e := range got {
		if e.Type != schema.EventRouteSelected {
			t.Errorf("type = %q, want %q", e.Type, schema.EventRouteSelected)
		}
		data, ok := e.Data.(schema.RouteSelectedData)
		if !ok {
			t.Fatalf("data type = %T", e.Data)
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		s := string(raw)
		for _, leak := range []string{"sk-super-secret", good.URL, "internal-tag", "api_key", "base_url"} {
			if strings.Contains(s, leak) {
				t.Errorf("payload leaked %q: %s", leak, s)
			}
		}
		if data.Alias != "primary" {
			t.Errorf("alias = %q, want primary", data.Alias)
		}
		if data.Reason == "" || data.Strategy == "" {
			t.Errorf("missing reason/strategy: %+v", data)
		}
	}
}
