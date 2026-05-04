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

package qdrant

// filter mirrors qdrant's `Filter` JSON shape. Only the conjunctive
// "must" branch is generated here; "should" / "must_not" are out of
// scope for the MetadataEquals translation.
type filter struct {
	Must []fieldCondition `json:"must,omitempty"`
}

// fieldCondition is one condition in filter.Must. Match.Value is the
// JSON-encoded equality target.
type fieldCondition struct {
	Key   string     `json:"key"`
	Match matchValue `json:"match"`
}

// matchValue is qdrant's `match.value` payload. The JSON-encoded shape
// dispatches on type at the server: numbers, booleans, and strings
// are first-class. We delegate type discrimination to the JSON encoder
// by leaving Value as `any`.
type matchValue struct {
	Value any `json:"value"`
}

// buildFilter translates a vector.SearchOptions.MetadataEquals map into
// a qdrant filter. Returns nil when the input is empty so the caller
// can omit the filter field entirely.
//
// Type rules: each key/value becomes one `must.match.value`. qdrant's
// match condition supports keyword (string), integer, and bool. Other
// types (slice, struct, map) cannot be expressed via match — the
// caller must fall back to Predicate, which we apply client-side.
func buildFilter(eq map[string]any) *filter {
	if len(eq) == 0 {
		return nil
	}
	conds := make([]fieldCondition, 0, len(eq))
	for k, v := range eq {
		if !isMatchable(v) {
			// Skip: qdrant cannot match this type. The store applies
			// MetadataEquals client-side as a fallback (see store.Search).
			continue
		}
		conds = append(conds, fieldCondition{
			Key:   k,
			Match: matchValue{Value: v},
		})
	}
	if len(conds) == 0 {
		return nil
	}
	return &filter{Must: conds}
}

// isMatchable reports whether v can be expressed as a qdrant
// match.value. Strings, integers, floats, and booleans round-trip
// through JSON cleanly. Anything else (slice, map, struct, nil) is
// rejected so we do not generate filters qdrant will silently return as
// empty results.
func isMatchable(v any) bool {
	switch v.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	default:
		return false
	}
}
