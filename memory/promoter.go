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

package memory

import "context"

// Promoter decides which working memory entries are promoted to session memory.
type Promoter interface {
	Promote(ctx context.Context, entries []Entry) ([]Entry, error)
}

// PromoteFunc is a function adapter for Promoter.
type PromoteFunc func(ctx context.Context, entries []Entry) ([]Entry, error)

// Promote implements Promoter.
func (f PromoteFunc) Promote(ctx context.Context, entries []Entry) ([]Entry, error) {
	return f(ctx, entries)
}

// PromoteAll returns a promoter that promotes all entries.
func PromoteAll() Promoter {
	return PromoteFunc(func(_ context.Context, entries []Entry) ([]Entry, error) {
		return entries, nil
	})
}

// PromoteNone returns a promoter that promotes no entries.
func PromoteNone() Promoter {
	return PromoteFunc(func(_ context.Context, _ []Entry) ([]Entry, error) {
		return nil, nil
	})
}
