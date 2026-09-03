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

	"github.com/vogo/vage/largemodel/router"
	"github.com/vogo/vage/schema"
)

// emitRouteSelected turns a protocol-neutral routing decision into a
// schema.Event and delivers it through whatever observer is bound on ctx.
// Failure to deliver must not change the routing result.
func emitRouteSelected(ctx context.Context, sel router.RouteSelection) {
	schema.DispatchEvent(ctx, schema.NewEvent(
		schema.EventRouteSelected,
		"",
		schema.SessionIDFromContext(ctx),
		schema.RouteSelectedData{
			Alias:    sel.Alias,
			Strategy: string(sel.Strategy),
			Reason:   string(sel.Reason),
			Stream:   sel.Stream,
		},
	))
}
