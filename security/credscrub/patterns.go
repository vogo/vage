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

import "regexp"

// DefaultRules returns the v1 text rule pack. Patterns are drawn from the
// public detection libraries (gitleaks, TruffleHog) and normalized to Go
// RE2 syntax. Keyword-gated rules (aws_secret_key, generic_api_key)
// require a nearby keyword to fire, reducing false positives.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:    "aws_access_key",
			Type:    "aws_access_key",
			Pattern: regexp.MustCompile(`\b(?:AKIA|ASIA|AROA|AGPA|AIDA|ANPA|ANVA|ABIA|ACCA)[0-9A-Z]{16}\b`),
		},
		{
			Name:     "aws_secret_key",
			Type:     "aws_secret_key",
			Pattern:  regexp.MustCompile(`\b[0-9a-zA-Z/+]{40}\b`),
			Keywords: []string{"aws_secret", "secret_access_key", "aws_secret_access_key"},
		},
		{
			Name:    "github_token",
			Type:    "github_token",
			Pattern: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,255}\b`),
		},
		{
			Name:    "slack_token",
			Type:    "slack_token",
			Pattern: regexp.MustCompile(`\bxox[baprsAPE]-[0-9A-Za-z-]{10,48}\b`),
		},
		{
			Name:    "jwt",
			Type:    "jwt",
			Pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		},
		{
			Name:    "pem_private_key",
			Type:    "pem_private_key",
			Pattern: regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY-----`),
		},
		{
			Name:    "stripe_key",
			Type:    "stripe_key",
			Pattern: regexp.MustCompile(`\bsk_(?:live|test)_[0-9a-zA-Z]{24,}\b`),
		},
		{
			Name:    "google_api_key",
			Type:    "google_api_key",
			Pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		},
		{
			Name:    "openai_key",
			Type:    "openai_key",
			Pattern: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}\b`),
		},
		{
			Name:    "bearer_token",
			Type:    "bearer_token",
			Pattern: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),
		},
		{
			Name:     "generic_api_key",
			Type:     "generic_api_key",
			Pattern:  regexp.MustCompile(`\b[A-Za-z0-9]{32,64}\b`),
			Keywords: []string{"api_key", "apikey", "access_token", "secret"},
		},
	}
}

// DefaultFieldRules returns rules that trigger when a JSON key's name
// matches a sensitive pattern. The entire string value at that key is
// redacted.
func DefaultFieldRules() []FieldRule {
	return []FieldRule{
		{
			Name:    "field_password",
			Type:    "password",
			Pattern: regexp.MustCompile(`(?i)^(?:password|passwd|pwd)$`),
		},
		{
			Name:    "field_secret",
			Type:    "secret",
			Pattern: regexp.MustCompile(`(?i)^.*(?:secret|credentials?)$`),
		},
		{
			Name:    "field_token",
			Type:    "token",
			Pattern: regexp.MustCompile(`(?i)^.*(?:token|auth[_\-]?token)$`),
		},
		{
			Name:    "field_api_key",
			Type:    "api_key",
			Pattern: regexp.MustCompile(`(?i)^(?:api[_\-]?key|access[_\-]?key|access[_\-]?token|auth[_\-]?header)$`),
		},
		{
			Name:    "field_authorization",
			Type:    "authorization",
			Pattern: regexp.MustCompile(`(?i)^(?:authorization|x-api-key|proxy-authorization)$`),
		},
	}
}

// DefaultAllowlist returns regexes that drop matches whose entire text
// matches the allow pattern. These reduce false positives on UUIDs and
// git commit hashes that otherwise collide with credential regexes.
func DefaultAllowlist() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`),
		regexp.MustCompile(`^[0-9a-f]{40}$`),
	}
}
