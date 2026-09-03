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

import (
	"encoding/base64"
	"strings"
)

// encodeScopeID returns the unpadded base64url encoding of id's UTF-8
// bytes. Arbitrary ID content therefore cannot inject the ':' delimiter
// or form a prefix collision with a sibling scope.
func encodeScopeID(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

// keyPrefix is the physical Store prefix for this tier and identity.
// Session and working tiers carry both identity segments; the long-term
// store tier leaves them empty so facts stay cross-session without
// sharing a prefix with session data or unkeyed backend records.
func (b *memoryBase) keyPrefix() string {
	return "mem:" + string(b.scope) + ":" + encodeScopeID(b.agentID) + ":" + encodeScopeID(b.sessionID) + ":"
}

func (b *memoryBase) physicalKey(logical string) string {
	return b.keyPrefix() + logical
}

func (b *memoryBase) logicalKey(physical string) string {
	prefix := b.keyPrefix()
	if !strings.HasPrefix(physical, prefix) {
		return physical
	}
	return physical[len(prefix):]
}
