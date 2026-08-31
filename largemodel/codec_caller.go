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
	"errors"

	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// ErrEmptyResponse reports a successful vendor response that carried no
// content, leaving nothing for the agent to act on. It is the codecs' own
// sentinel re-exported here, so errors.Is keeps matching whichever protocol
// produced it.
var ErrEmptyResponse = modelcore.ErrEmptyResponse

// codecCaller is the single adapter between the public Caller contract and a
// protocol codec. It is protocol-neutral by construction: it copies envelope
// fields, re-types the finish reason, wraps the codec's classified failure in
// the public APIError, and hands the codec's decoded event source to Stream
// for the lifecycle guarantees. It never inspects a vendor field, an event
// type, a stop reason or an error body — every one of those decisions belongs
// to the codec it wraps.
type codecCaller struct {
	codec modelcore.Codec
}

// Protocol implements Caller.
func (c *codecCaller) Protocol() schema.Protocol { return c.codec.Protocol() }

// Call implements Caller.
func (c *codecCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	result, err := c.codec.Call(ctx, codecRequest(req))
	if err != nil {
		return nil, apiError(err)
	}

	return &Response{
		ID:           result.ID,
		Model:        result.Model,
		Message:      result.Message,
		FinishReason: FinishReason(result.FinishReason),
		Usage:        result.Usage,
	}, nil
}

// CallStream implements Caller.
func (c *codecCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	stream, err := c.codec.CallStream(ctx, codecRequest(req))
	if err != nil {
		return nil, apiError(err)
	}

	return NewStream(func() (*Chunk, error) {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			return nil, apiError(recvErr)
		}

		return publicChunk(chunk), nil
	}, stream.Close), nil
}

// codecRequest projects the public request onto the codec bridge. Slices are
// shared rather than copied: a codec treats the request as read-only, exactly
// as the middleware chain above already does.
func codecRequest(req *Request) *modelcore.Request {
	return &modelcore.Request{
		Model:          req.Model,
		Messages:       req.Messages,
		Tools:          req.Tools,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		Stop:           req.Stop,
		PromptCaching:  req.PromptCaching,
		ResponseSchema: req.ResponseSchema,
	}
}

// publicChunk projects a decoded chunk onto the public envelope.
func publicChunk(chunk *modelcore.Chunk) *Chunk {
	if chunk == nil {
		return nil
	}

	out := &Chunk{
		TextDelta:     chunk.TextDelta,
		ThinkingDelta: chunk.ThinkingDelta,
		FinishReason:  FinishReason(chunk.FinishReason),
		Usage:         chunk.Usage,
	}

	for _, delta := range chunk.ToolCallDeltas {
		out.ToolCallDeltas = append(out.ToolCallDeltas, ToolCallDelta{
			Index:          delta.Index,
			ID:             delta.ID,
			Name:           delta.Name,
			ArgumentsDelta: delta.ArgumentsDelta,
		})
	}

	return out
}

// apiError lifts a codec's classified failure into the public APIError,
// keeping the vendor error underneath reachable through Unwrap. Errors the
// codec did not classify — transport failures, a cancelled context, io.EOF at
// the end of a stream — pass through untouched.
func apiError(err error) error {
	var classified *modelcore.APIError
	if !errors.As(err, &classified) {
		return err
	}

	return &APIError{
		StatusCode: classified.StatusCode,
		Code:       classified.Code,
		Type:       classified.Type,
		Message:    classified.Message,
		Err:        classified.Err,
	}
}

var _ Caller = (*codecCaller)(nil)
