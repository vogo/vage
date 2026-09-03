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

package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/vogo/vage/largemodel"
)

func TestMiddleware_ForwardsCapabilities(t *testing.T) {
	fake := &largemodel.FakeCaller{
		DeclaredSet: true,
		Declared:    largemodel.Capabilities{ToolCalling: largemodel.SupportNative},
		Endpoints: []largemodel.EndpointCapability{{
			Alias:        "only",
			Capabilities: largemodel.Capabilities{ToolCalling: largemodel.SupportNative},
		}},
	}

	wrapped := NewTimeoutMiddleware(time.Second).Wrap(fake)
	provider, ok := wrapped.(largemodel.CapabilityProvider)
	if !ok {
		t.Fatal("wrapped caller must implement CapabilityProvider")
	}

	got, err := provider.Capabilities(context.Background(), &largemodel.Request{})
	if err != nil {
		t.Fatal(err)
	}

	if got.ToolCalling != largemodel.SupportNative {
		t.Fatalf("got %+v", got)
	}

	endpoints, ok := wrapped.(largemodel.EndpointCapabilityProvider)
	if !ok || len(endpoints.EndpointCapabilities()) != 1 {
		t.Fatalf("endpoint capabilities not forwarded")
	}
}
