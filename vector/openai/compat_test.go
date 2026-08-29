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

// Tests for the deprecated compatibility path. They exist to prove the
// promise made in the package doc: old imports still compile, and they
// still produce values indistinguishable from the new path's.
package openai

import (
	"errors"
	"testing"

	"github.com/vogo/vage/vector"
	"github.com/vogo/vage/vector/provider/openais"
)

func TestAliasesAreTheSameTypes(t *testing.T) {
	// Assigning across the two import paths only compiles if these are
	// aliases rather than distinct declared types — that is what keeps
	// callers' type assertions working after the move.
	var (
		mine   *Embedder
		theirs *openais.Embedder
	)
	mine = theirs
	theirs = mine
	_ = mine
	_ = theirs

	// An openais option must land in a slice of the deprecated Option
	// type, and vice versa — options cross the two paths in both
	// directions, so a half-migrated call site still compiles.
	mixed := []Option{openais.WithModel("text-embedding-3-large"), WithAPIKey("k")}
	if _, err := openais.New(append(mixed, openais.WithBaseURL("http://x"))...); err != nil {
		t.Fatalf("options from the deprecated path rejected by openais.New: %v", err)
	}
}

func TestForwardedConstructorMatchesNewPath(t *testing.T) {
	e, err := New(WithBaseURL("http://x"), WithAPIKey("k"), WithModel("text-embedding-3-large"), WithMaxInputTokens(4096))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.ModelName() != "text-embedding-3-large" {
		t.Errorf("ModelName = %q", e.ModelName())
	}
	if e.MaxInputTokens() != 4096 {
		t.Errorf("MaxInputTokens = %d", e.MaxInputTokens())
	}

	// The value the deprecated constructor returns must satisfy the same
	// capability set as before the move.
	var emb vector.Embedder = e
	if _, ok := emb.(vector.BatchEmbedder); !ok {
		t.Error("deprecated path lost BatchEmbedder")
	}
	if _, ok := emb.(vector.NamedEmbedder); !ok {
		t.Error("deprecated path lost NamedEmbedder")
	}
	if _, ok := emb.(vector.LimitedEmbedder); !ok {
		t.Error("deprecated path lost LimitedEmbedder")
	}
}

func TestErrorIdentityPreserved(t *testing.T) {
	_, err := New()
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
	// Same instance, so code that compares against either spelling keeps
	// matching after a partial migration.
	if !errors.Is(err, openais.ErrMissingAPIKey) {
		t.Fatalf("deprecated sentinel no longer matches openais.ErrMissingAPIKey: %v", err)
	}
}

func TestConstantsMatch(t *testing.T) {
	if DefaultBaseURL != openais.DefaultBaseURL ||
		DefaultModel != openais.DefaultModel ||
		MaxInputTokensTextEmbedding3 != openais.MaxInputTokensTextEmbedding3 {
		t.Fatal("deprecated constants drifted from openais")
	}
}
