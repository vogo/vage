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

package orchestrate

import (
	"context"
	"sort"

	"github.com/vogo/largemodel/schema"
)

// Aggregator merges terminal node results into a single response.
type Aggregator interface {
	Aggregate(ctx context.Context, results map[string]*schema.RunResponse) (*schema.RunResponse, error)
}

type lastResultAggregator struct{}

// LastResultAggregator returns an Aggregator that picks the last terminal node result by sorted node ID.
func LastResultAggregator() Aggregator {
	return &lastResultAggregator{}
}

func (a *lastResultAggregator) Aggregate(_ context.Context, results map[string]*schema.RunResponse) (*schema.RunResponse, error) {
	if len(results) == 0 {
		return &schema.RunResponse{}, nil
	}
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return results[keys[len(keys)-1]], nil
}

type concatMessagesAggregator struct{}

// ConcatMessagesAggregator returns an Aggregator that concatenates messages from all terminal nodes ordered by sorted node ID.
func ConcatMessagesAggregator() Aggregator {
	return &concatMessagesAggregator{}
}

func (a *concatMessagesAggregator) Aggregate(_ context.Context, results map[string]*schema.RunResponse) (*schema.RunResponse, error) {
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resp := &schema.RunResponse{}
	for _, k := range keys {
		if v := results[k]; v != nil {
			resp.Messages = append(resp.Messages, v.Messages...)
			if resp.SessionID == "" {
				resp.SessionID = v.SessionID
				resp.Metadata = v.Metadata
			}
		}
	}
	return resp, nil
}
