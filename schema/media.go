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

package schema

// ImageFromURL builds a canonical image part from a remote URL. It does not
// fetch the URL. An empty URL is rejected because Message.Validate requires
// exactly one media source.
func ImageFromURL(url string) (MessagePart, error) {
	return validatedMediaPart(MessagePart{Type: MessagePartImage, URL: url})
}

// ImageFromBytes builds a canonical image part from inline bytes. mimeType is
// required and must be an image/* type. The bytes are copied so later changes
// to data cannot mutate the part.
func ImageFromBytes(data []byte, mimeType string) (MessagePart, error) {
	return validatedMediaPart(MessagePart{
		Type:     MessagePartImage,
		Data:     append([]byte(nil), data...),
		MimeType: mimeType,
	})
}

// FileFromID builds a canonical file part from a provider-hosted file id. It
// does not upload or manage the file. An empty id is rejected.
func FileFromID(id string) (MessagePart, error) {
	return validatedMediaPart(MessagePart{Type: MessagePartFile, FileID: id})
}

// FileFromBytes builds a canonical file part from inline bytes. mimeType is
// required; filename may be empty so Anthropic callers can omit it (OpenAI's
// codec still rejects an unnamed inline file before the backend). The bytes
// are copied so later changes to data cannot mutate the part.
func FileFromBytes(data []byte, mimeType, filename string) (MessagePart, error) {
	return validatedMediaPart(MessagePart{
		Type:     MessagePartFile,
		Data:     append([]byte(nil), data...),
		MimeType: mimeType,
		Filename: filename,
	})
}

// validatedMediaPart runs the same Message.Validate rules a codec will see,
// using a user message because image and file parts are only valid there.
func validatedMediaPart(part MessagePart) (MessagePart, error) {
	msg := NewUserMessageWithParts(ProtocolOpenAIChat, []MessagePart{part})
	if err := msg.Validate(); err != nil {
		return MessagePart{}, err
	}

	return msg.Parts()[0], nil
}
