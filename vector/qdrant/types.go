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

// JSON DTOs for the qdrant REST API. Field names and shapes are pinned
// to the documented v1.x contract — keep this file boring and let the
// translation logic live in store.go / filter.go.

// vectorParams is the body of `PUT /collections/{name}` "vectors" field.
// Only Size + Distance are surfaced; on-disk / quantization options are
// out of scope for this package.
type vectorParams struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

// createCollectionRequest is the body of `PUT /collections/{name}`.
type createCollectionRequest struct {
	Vectors vectorParams `json:"vectors"`
}

// upsertPointsRequest is the body of `PUT /collections/{name}/points`.
// Wait=true so the client can read its own writes immediately, which is
// what callers of vector.VectorStore expect.
type upsertPointsRequest struct {
	Points []point `json:"points"`
}

// point is one element of upsertPointsRequest.Points.
type point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

// searchRequest is the body of `POST /collections/{name}/points/search`.
type searchRequest struct {
	Vector      []float32 `json:"vector"`
	Limit       int       `json:"limit"`
	WithPayload bool      `json:"with_payload"`
	WithVector  bool      `json:"with_vector"`
	Filter      *filter   `json:"filter,omitempty"`
	ScoreThresh *float32  `json:"score_threshold,omitempty"`
}

// searchResponse is the response of /points/search.
type searchResponse struct {
	Result []scoredPoint `json:"result"`
	Status string        `json:"status"`
	Time   float64       `json:"time"`
}

type scoredPoint struct {
	ID      string         `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload,omitempty"`
	Vector  []float32      `json:"vector,omitempty"`
}

// deletePointsRequest is the body of
// `POST /collections/{name}/points/delete`.
type deletePointsRequest struct {
	Points []string `json:"points"`
}

// scrollRequest is the body of
// `POST /collections/{name}/points/scroll` (used by List).
type scrollRequest struct {
	Limit       int    `json:"limit"`
	Offset      string `json:"offset,omitempty"`
	WithPayload bool   `json:"with_payload"`
	WithVector  bool   `json:"with_vector"`
}

type scrollResponse struct {
	Result struct {
		Points         []scoredPoint `json:"points"`
		NextPageOffset *string       `json:"next_page_offset"`
	} `json:"result"`
}

// genericResponse decodes the envelope for endpoints that do not return
// data (create collection, upsert, delete). Used to surface the
// per-request `status` string in error messages.
type genericResponse struct {
	Status any     `json:"status"`
	Time   float64 `json:"time"`
}
