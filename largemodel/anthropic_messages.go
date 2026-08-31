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

import "github.com/vogo/vage/largemodel/provider/anthropics"

// AnthropicMessagesBackend is the method set an Anthropic Messages backend
// must provide. It is exactly what *anthropic.Client offers and exactly what
// the provider's routed ComposeClient offers, so a single-endpoint client and
// a multi-endpoint pool are interchangeable behind the same Caller.
//
// As on the OpenAI side, the method set is owned by the provider package and
// aliased here, so a backend written against either name satisfies both.
type AnthropicMessagesBackend = anthropics.Messenger

// newAnthropicMessagesCaller adapts an Anthropic Messages backend to the
// public Caller contract. The structural differences Anthropic imposes —
// hoisted system text, content blocks, mandatory max_tokens, index-addressed
// stream blocks — are all absorbed by the provider codec.
func newAnthropicMessagesCaller(backend AnthropicMessagesBackend) Caller {
	return &codecCaller{codec: anthropics.NewMessagesCodec(backend)}
}
