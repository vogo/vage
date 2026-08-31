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

import "github.com/vogo/vage/largemodel/provider/openais"

// OpenAIChatBackend is the method set an OpenAI Chat Completions backend must
// provide. It is exactly what *openai.Client offers and exactly what the
// provider's routed ComposeClient offers, so a single-endpoint client and a
// multi-endpoint pool are interchangeable behind the same Caller.
//
// The method set is owned by the provider package, which is where the wire
// types it names belong; this is an alias, so a backend written against either
// name satisfies both.
type OpenAIChatBackend = openais.ChatCompleter

// newOpenAIChatCaller adapts an OpenAI Chat Completions backend to the public
// Caller contract. Everything protocol-specific — request assembly, response
// and usage normalization, stream decoding, error classification — lives in
// the provider codec; this file only names the seam.
func newOpenAIChatCaller(backend OpenAIChatBackend) Caller {
	return &codecCaller{codec: openais.NewChatCodec(backend)}
}
