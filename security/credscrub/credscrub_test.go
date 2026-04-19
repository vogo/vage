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

package credscrub

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// newTestScanner returns a Scanner with default rules and the given action.
func newTestScanner(t *testing.T, action Action) *Scanner {
	t.Helper()

	return NewScanner(Config{Action: action})
}

func TestDefaultRules_PositiveMatches(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	cases := []struct {
		name string
		text string
		want string // expected credential type in the hit
	}{
		{"aws_access_key", "AKIAIOSFODNN7EXAMPLE", "aws_access_key"},
		{"aws_secret_key_with_keyword", "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "aws_secret_key"},
		{"github_token_ghp", "ghp_1234567890abcdefghij1234567890abcdefgh", "github_token"},
		{"github_token_ghu", "ghu_abcdefghijklmnopqrstuvwxyz0123456789AB", "github_token"},
		{"slack_bot", "xoxb-12345678901-12345678901-abcdefghij", "slack_token"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.SflKxwRJSMeKKF2QT4fwpMeJf36POk", "jwt"},
		{"pem_rsa", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow", "pem_private_key"},
		{"pem_pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIEvQ", "pem_private_key"},
		{"stripe_live", "sk_" + "live_" + "abc123def456ghi789jkl012", "stripe_key"},
		{"stripe_test", "sk_" + "test_" + "abc123def456ghi789jkl012mno345", "stripe_key"},
		{"google_api_key", "AIzaSyA12345678901234567890123456789012", "google_api_key"},
		{"openai_key", "sk-abcdefghijklmnopqrstuvwxyz0123", "openai_key"},
		{"bearer_token", "Authorization: Bearer abcdefghijklmnop0123456789", "bearer_token"},
		{"generic_keyword", "api_key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "generic_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := s.ScanText(tc.text)
			if len(r.Hits) == 0 {
				t.Fatalf("expected at least one hit in %q", tc.text)
			}

			found := false
			for _, h := range r.Hits {
				if h.Type == tc.want {
					found = true

					break
				}
			}

			if !found {
				types := make([]string, 0, len(r.Hits))
				for _, h := range r.Hits {
					types = append(types, h.Type)
				}

				t.Errorf("want hit type %q in %v", tc.want, types)
			}
		})
	}
}

func TestDefaultRules_NegativeMatches(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	cases := []struct {
		name string
		text string
	}{
		{"aws_too_short", "AKIA123"},
		{"aws_lowercase", "akiaiosfodnn7example"},
		{"secret_without_keyword", "randomstringaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"jwt_two_segments_only", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9"},
		{"plain_english", "the quick brown fox jumps over the lazy dog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := s.ScanText(tc.text)
			if len(r.Hits) > 0 {
				t.Errorf("expected no hits in %q, got %d (%+v)", tc.text, len(r.Hits), r.Hits)
			}
		})
	}
}

func TestAllowlist_UUIDv4(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	uuid := "550e8400-e29b-41d4-a716-446655440000"
	r := s.ScanText("id=" + uuid)

	for _, h := range r.Hits {
		if strings.Contains(h.Masked, "550e") {
			t.Errorf("UUID should have been allowlisted, but was caught by rule %q", h.Rule)
		}
	}
}

func TestAllowlist_GitSHA(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	// 40-hex git sha embedded in a secret_access_key context to tempt aws_secret_key.
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	text := "aws_secret_access_key=" + sha

	r := s.ScanText(text)
	// Should NOT match aws_secret_key — sha is allowlisted.
	for _, h := range r.Hits {
		if h.Type == "aws_secret_key" {
			t.Errorf("git sha should have been allowlisted but matched aws_secret_key: %+v", h)
		}
	}
}

func TestKeywordGated_AWSSecret(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	base64LikeNotASha := "wJalrXUtnFEMIABCDENGKbPxRfiCYEXAMPLEKEY0"
	withKey := "aws_secret=" + base64LikeNotASha
	withoutKey := base64LikeNotASha

	r1 := s.ScanText(withKey)
	if !hasType(r1.Hits, "aws_secret_key") {
		t.Errorf("expected aws_secret_key when keyword is present; got %+v", r1.Hits)
	}

	r2 := s.ScanText(withoutKey)
	if hasType(r2.Hits, "aws_secret_key") {
		t.Errorf("did NOT expect aws_secret_key without keyword; got %+v", r2.Hits)
	}
}

func TestScanJSON_FieldRuleRedact(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	input := `{"username":"alice","password":"hunter2","nested":{"api_key":"AKIAIOSFODNN7EXAMPLE"}}`

	res, redacted, err := s.ScanJSON([]byte(input))
	if err != nil {
		t.Fatalf("ScanJSON err: %v", err)
	}

	if len(res.Hits) < 2 {
		t.Fatalf("expected >=2 hits (password field + api_key field), got %d: %+v", len(res.Hits), res.Hits)
	}

	if !strings.Contains(string(redacted), "[REDACTED:password]") {
		t.Errorf("expected password redaction; got %s", string(redacted))
	}

	if !strings.Contains(string(redacted), "[REDACTED:api_key]") {
		t.Errorf("expected api_key redaction; got %s", string(redacted))
	}

	if strings.Contains(string(redacted), "hunter2") {
		t.Errorf("plaintext password leaked into redacted output: %s", string(redacted))
	}

	if strings.Contains(string(redacted), "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("plaintext aws key leaked into redacted output: %s", string(redacted))
	}
}

func TestScanJSON_AuthorizationField(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	input := `{"headers":{"Authorization":"Bearer abcdefghijklmnop0123456789"}}`

	res, redacted, err := s.ScanJSON([]byte(input))
	if err != nil {
		t.Fatalf("ScanJSON err: %v", err)
	}

	if !hasType(res.Hits, "authorization") {
		t.Errorf("expected field-rule authorization hit; got %+v", res.Hits)
	}

	if strings.Contains(string(redacted), "abcdefghijklmnop") {
		t.Errorf("plaintext bearer token leaked: %s", string(redacted))
	}
}

func TestScanJSON_UsernameUntouched(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	input := `{"username":"alice","greeting":"hello world"}`

	res, redacted, err := s.ScanJSON([]byte(input))
	if err != nil {
		t.Fatalf("ScanJSON err: %v", err)
	}

	if len(res.Hits) != 0 {
		t.Errorf("expected no hits on benign JSON, got %+v", res.Hits)
	}

	if redacted != nil {
		t.Errorf("expected nil redacted when no hits; got %q", string(redacted))
	}
}

func TestScanJSON_InvalidJSONFallsBackToText(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	// Not valid JSON, but contains a credential.
	input := []byte("not-json: ghp_1234567890abcdefghij1234567890abcdefgh")

	res, redacted, err := s.ScanJSON(input)
	if err != nil {
		t.Fatalf("ScanJSON err: %v", err)
	}

	if !hasType(res.Hits, "github_token") {
		t.Errorf("expected github_token hit on invalid-JSON fallback; got %+v", res.Hits)
	}

	if !strings.Contains(string(redacted), "[REDACTED:github_token]") {
		t.Errorf("expected redaction marker in fallback; got %s", string(redacted))
	}
}

func TestScanJSONMap_InPlaceRedact(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	m := map[string]any{
		"password": "hunter2",
		"normal":   "ok",
	}

	res := s.ScanJSONMap(m)
	if len(res.Hits) == 0 {
		t.Fatalf("expected hit on password")
	}

	if m["password"] != "[REDACTED:password]" {
		t.Errorf("expected in-place redaction, got %v", m["password"])
	}

	if m["normal"] != "ok" {
		t.Errorf("non-sensitive field should not change, got %v", m["normal"])
	}
}

func TestScanJSONMap_LogDoesNotMutate(t *testing.T) {
	s := newTestScanner(t, ActionLog)

	m := map[string]any{"password": "hunter2"}

	res := s.ScanJSONMap(m)
	if len(res.Hits) == 0 {
		t.Fatalf("expected hit even with log action")
	}

	if m["password"] != "hunter2" {
		t.Errorf("log action should not mutate; got %v", m["password"])
	}
}

func TestRedactText_MultipleHits(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	text := "first=AKIAIOSFODNN7EXAMPLE second=ghp_1234567890abcdefghij1234567890abcdefgh done"
	r := s.ScanText(text)
	out := s.RedactText(text, r.Hits)

	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key leaked: %s", out)
	}

	if strings.Contains(out, "ghp_1234567890abcdefghij1234567890abcdefgh") {
		t.Errorf("GitHub token leaked: %s", out)
	}

	if !strings.Contains(out, "[REDACTED:aws_access_key]") {
		t.Errorf("expected aws_access_key redaction; got %s", out)
	}

	if !strings.Contains(out, "[REDACTED:github_token]") {
		t.Errorf("expected github_token redaction; got %s", out)
	}

	if !strings.Contains(out, "first=") || !strings.Contains(out, "second=") || !strings.Contains(out, "done") {
		t.Errorf("surrounding text lost: %s", out)
	}
}

func TestRedactText_Empty(t *testing.T) {
	s := newTestScanner(t, ActionRedact)
	if got := s.RedactText("hello", nil); got != "hello" {
		t.Errorf("expected passthrough on empty hits; got %q", got)
	}
}

func TestMaxScanBytes_Truncation(t *testing.T) {
	s := NewScanner(Config{
		MaxScanBytes: 32,
		Action:       ActionRedact,
	})

	// AWS key placed beyond the 32-byte cap -> should not be detected.
	prefix := strings.Repeat("a", 50)
	text := prefix + "AKIAIOSFODNN7EXAMPLE"

	r := s.ScanText(text)
	if hasType(r.Hits, "aws_access_key") {
		t.Errorf("hit beyond MaxScanBytes should be invisible: %+v", r.Hits)
	}

	if !r.Truncated {
		t.Errorf("expected Truncated=true when input exceeds MaxScanBytes")
	}
}

func TestMaskCredential_NoPlaintextLeak(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	secret := "ghp_1234567890abcdefghij1234567890abcdefgh"
	r := s.ScanText(secret)
	if len(r.Hits) == 0 {
		t.Fatalf("expected hit")
	}

	for _, h := range r.Hits {
		if strings.Contains(h.Masked, "abcdefghij") {
			t.Errorf("Masked leaked mid-secret content: %q", h.Masked)
		}

		if len(h.Masked) > 8 {
			t.Errorf("Masked too long (>8 chars): %q", h.Masked)
		}
	}
}

func TestNilScanner_NoPanic(t *testing.T) {
	var s *Scanner

	res := s.ScanText("AKIAIOSFODNN7EXAMPLE")
	if len(res.Hits) != 0 {
		t.Errorf("nil scanner should produce no hits; got %+v", res.Hits)
	}

	res2 := s.ScanJSONMap(map[string]any{"password": "x"})
	if len(res2.Hits) != 0 {
		t.Errorf("nil scanner should produce no hits on map; got %+v", res2.Hits)
	}

	resJSON, redacted, err := s.ScanJSON([]byte(`{"a":"b"}`))
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}

	if len(resJSON.Hits) != 0 || redacted != nil {
		t.Errorf("nil scanner should be a no-op on ScanJSON")
	}
}

func TestConfig_EmptyRulesDisablesTextScan(t *testing.T) {
	s := NewScanner(Config{
		Rules:     []Rule{},
		Allowlist: []*regexp.Regexp{},
	})

	r := s.ScanText("AKIAIOSFODNN7EXAMPLE")
	if len(r.Hits) > 0 {
		t.Errorf("empty rules slice should disable text scanning; got %+v", r.Hits)
	}
}

func TestConcurrency(t *testing.T) {
	s := newTestScanner(t, ActionRedact)
	text := "key=AKIAIOSFODNN7EXAMPLE token=ghp_1234567890abcdefghij1234567890abcdefgh"

	done := make(chan struct{}, 10)
	for range 10 {
		go func() {
			for range 100 {
				r := s.ScanText(text)
				if len(r.Hits) < 2 {
					t.Errorf("unexpected hit count %d", len(r.Hits))
				}
			}
			done <- struct{}{}
		}()
	}

	for range 10 {
		<-done
	}
}

func TestSummarizeHelpers(t *testing.T) {
	hits := []Hit{
		{Type: "aws_access_key", Masked: "AKIA****"},
		{Type: "github_token", Masked: "ghp_****"},
		{Type: "aws_access_key", Masked: "AKIA****"}, // duplicate
	}

	types := SummarizeTypes(hits)
	if len(types) != 2 {
		t.Errorf("expected 2 unique types; got %v", types)
	}

	if types[0] != "aws_access_key" || types[1] != "github_token" {
		t.Errorf("expected sorted types; got %v", types)
	}

	masked := SummarizeMasked(hits)
	if len(masked) != 2 {
		t.Errorf("expected 2 unique masked; got %v", masked)
	}
}

func TestJSONRoundTripStructurePreserved(t *testing.T) {
	s := newTestScanner(t, ActionRedact)

	input := `{"user":{"id":42,"password":"hunter2","tags":["admin","reviewer"]}}`

	_, redacted, err := s.ScanJSON([]byte(input))
	if err != nil {
		t.Fatalf("ScanJSON err: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(redacted, &out); err != nil {
		t.Fatalf("redacted JSON invalid: %v", err)
	}

	user := out["user"].(map[string]any)
	if user["id"].(float64) != 42 {
		t.Errorf("non-string value mutated: %v", user["id"])
	}

	if user["password"] != "[REDACTED:password]" {
		t.Errorf("password not redacted: %v", user["password"])
	}

	tags := user["tags"].([]any)
	if len(tags) != 2 || tags[0] != "admin" {
		t.Errorf("slice mangled: %v", tags)
	}
}

func hasType(hits []Hit, want string) bool {
	for _, h := range hits {
		if h.Type == want {
			return true
		}
	}

	return false
}
