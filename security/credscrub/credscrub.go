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

// Package credscrub scans strings and JSON payloads for credentials
// (API tokens, access keys, private keys) and can redact or report them.
// It is intended as a middleware layer for the MCP client/server
// boundary, where third-party tool I/O is an attack surface.
package credscrub

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Action is the action taken when credentials are detected.
type Action string

const (
	// ActionLog records hits but leaves the content unchanged.
	ActionLog Action = "log"
	// ActionRedact replaces matched credentials with [REDACTED:<type>].
	ActionRedact Action = "redact"
	// ActionBlock rejects the call and signals the caller to abort.
	ActionBlock Action = "block"
)

// defaultMaxScanBytes caps the scanned text length.
const defaultMaxScanBytes = 256 * 1024

// defaultKeywordWindow is the distance (in bytes) before a match where
// keyword-gated rules look for their keyword.
const defaultKeywordWindow = 64

// Rule is a single credential pattern.
// If Keywords is non-empty, at least one must appear within KeywordWindow
// bytes before the match (lowercase compared).
type Rule struct {
	Name          string
	Type          string // placeholder tag, e.g. "aws_access_key"
	Pattern       *regexp.Regexp
	Keywords      []string
	KeywordWindow int // 0 means use defaultKeywordWindow
}

// FieldRule triggers when a JSON map key (case-insensitive) matches Pattern.
// The entire string value at that key is treated as a credential of Type.
type FieldRule struct {
	Name    string
	Type    string
	Pattern *regexp.Regexp
}

// Config configures a Scanner.
type Config struct {
	// Rules is the ordered list of text rules. If nil, DefaultRules() is used.
	// Pass an empty slice to disable text rules entirely.
	Rules []Rule
	// FieldRules is the list of JSON key rules. If nil, DefaultFieldRules() is used.
	// Pass an empty slice to disable field rules entirely.
	FieldRules []FieldRule
	// Allowlist drops matches whose text equals an allowlist hit.
	// If nil, DefaultAllowlist() is used. Pass an empty slice to disable.
	Allowlist []*regexp.Regexp
	// Action is the default action on hit: ActionLog / ActionRedact / ActionBlock.
	// Empty defaults to ActionRedact.
	Action Action
	// MaxScanBytes caps the scanned input length. 0 = default (256*1024).
	MaxScanBytes int
}

// Scanner scans strings and JSON for credentials.
// Zero value is not usable; build one with NewScanner.
// A Scanner is safe for concurrent use; it holds no mutable state.
type Scanner struct {
	rules        []Rule
	fieldRules   []FieldRule
	allow        []*regexp.Regexp
	action       Action
	maxScanBytes int
}

// NewScanner builds a Scanner from cfg.
func NewScanner(cfg Config) *Scanner {
	rules := cfg.Rules
	if rules == nil {
		rules = DefaultRules()
	}

	fieldRules := cfg.FieldRules
	if fieldRules == nil {
		fieldRules = DefaultFieldRules()
	}

	allow := cfg.Allowlist
	if allow == nil {
		allow = DefaultAllowlist()
	}

	action := cfg.Action
	if action == "" {
		action = ActionRedact
	}

	maxBytes := cfg.MaxScanBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxScanBytes
	}

	return &Scanner{
		rules:        rules,
		fieldRules:   fieldRules,
		allow:        allow,
		action:       action,
		maxScanBytes: maxBytes,
	}
}

// Hit describes a single credential match.
type Hit struct {
	// Rule is the rule name that matched.
	Rule string
	// Type is the placeholder type, e.g. "aws_access_key".
	Type string
	// Start and End are byte offsets in the scanned text.
	// For field-rule hits on a JSON key, Start and End both point to the
	// whole value (0..len(value)).
	Start int
	End   int
	// Masked is a short, de-identified preview (e.g. "AKIA****").
	// It never contains the full credential.
	Masked string
	// Field is the JSON key path when the hit came from ScanJSON/ScanJSONMap.
	// Empty for plain-text hits.
	Field string
}

// ScanResult is the outcome of a scan.
type ScanResult struct {
	Hits      []Hit
	Action    Action
	Truncated bool
}

// Action returns the scanner's configured default action.
// A nil Scanner returns ActionLog (the safe no-op value).
func (s *Scanner) Action() Action {
	if s == nil {
		return ActionLog
	}

	return s.action
}

// ScanText scans a plain string for credentials.
func (s *Scanner) ScanText(text string) ScanResult {
	return s.scanText(text, "")
}

func (s *Scanner) scanText(text, field string) ScanResult {
	if s == nil {
		return ScanResult{Action: ActionLog}
	}

	truncated := false
	if len(text) > s.maxScanBytes {
		text = text[:s.maxScanBytes]
		truncated = true
	}

	lowered := strings.ToLower(text)

	var hits []Hit
	for _, r := range s.rules {
		locs := r.Pattern.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			start, end := loc[0], loc[1]
			if !s.keywordOK(lowered, start, r) {
				continue
			}

			matched := text[start:end]
			if s.allowlisted(matched) {
				continue
			}

			hits = append(hits, Hit{
				Rule:   r.Name,
				Type:   r.Type,
				Start:  start,
				End:    end,
				Masked: maskCredential(matched),
				Field:  field,
			})
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Start < hits[j].Start
	})

	return ScanResult{
		Hits:      hits,
		Action:    s.action,
		Truncated: truncated,
	}
}

// keywordOK returns true if r has no keywords, or at least one keyword
// is present within KeywordWindow bytes before start in lowered.
func (s *Scanner) keywordOK(lowered string, start int, r Rule) bool {
	if len(r.Keywords) == 0 {
		return true
	}

	window := r.KeywordWindow
	if window <= 0 {
		window = defaultKeywordWindow
	}

	from := max(start-window, 0)

	prefix := lowered[from:start]
	for _, kw := range r.Keywords {
		if strings.Contains(prefix, strings.ToLower(kw)) {
			return true
		}
	}

	return false
}

func (s *Scanner) allowlisted(matched string) bool {
	for _, re := range s.allow {
		if re.MatchString(matched) {
			return true
		}
	}

	return false
}

// RedactText returns text with each hit's byte range replaced by
// [REDACTED:<type>]. Overlapping hits are de-duplicated (first-wins).
// If hits is empty, text is returned unchanged.
func (s *Scanner) RedactText(text string, hits []Hit) string {
	if len(hits) == 0 {
		return text
	}

	sorted := make([]Hit, len(hits))
	copy(sorted, hits)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Start < sorted[j].Start
	})

	var b strings.Builder
	b.Grow(len(text))

	cursor := 0
	for _, h := range sorted {
		if h.Start < cursor {
			continue
		}

		if h.Start > len(text) || h.End > len(text) || h.End <= h.Start {
			continue
		}

		b.WriteString(text[cursor:h.Start])
		fmt.Fprintf(&b, "[REDACTED:%s]", h.Type)
		cursor = h.End
	}

	b.WriteString(text[cursor:])

	return b.String()
}

// ScanJSON parses raw JSON and scans both keys (against FieldRules) and
// string values (against Rules). The returned redacted slice is a re-
// marshalled JSON with string values replaced where redaction applied;
// if no hits were found it is nil. If raw is not valid JSON, ScanJSON
// falls back to ScanText on raw and returns the raw + text-redaction.
func (s *Scanner) ScanJSON(raw []byte) (ScanResult, []byte, error) {
	if s == nil {
		return ScanResult{Action: ActionLog}, nil, nil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		res := s.ScanText(string(raw))
		if len(res.Hits) == 0 {
			return res, nil, nil
		}

		redacted := s.RedactText(string(raw), res.Hits)

		return res, []byte(redacted), nil
	}

	var hits []Hit
	truncated := false
	mutated := s.walkJSON(decoded, "", &hits, &truncated)

	if len(hits) == 0 {
		return ScanResult{Action: s.action, Truncated: truncated}, nil, nil
	}

	out, err := json.Marshal(mutated)
	if err != nil {
		return ScanResult{Hits: hits, Action: s.action, Truncated: truncated}, nil, fmt.Errorf("marshal redacted JSON: %w", err)
	}

	return ScanResult{Hits: hits, Action: s.action, Truncated: truncated}, out, nil
}

// ScanJSONMap scans a decoded JSON map in place. String leaves that need
// redaction are mutated to their redacted form when the scanner's action
// is ActionRedact; for ActionLog and ActionBlock the map is left unchanged
// and the caller decides what to do.
func (s *Scanner) ScanJSONMap(m map[string]any) ScanResult {
	if s == nil || m == nil {
		return ScanResult{Action: ActionLog}
	}

	var hits []Hit
	truncated := false
	mutateMap := s.action == ActionRedact
	s.walkMap(m, "", &hits, &truncated, mutateMap)

	return ScanResult{Hits: hits, Action: s.action, Truncated: truncated}
}

// walkJSON recursively walks a decoded value and returns a possibly
// substituted copy for redaction. Hits are appended to *hits. truncated
// is set if any leaf string exceeded MaxScanBytes.
func (s *Scanner) walkJSON(v any, path string, hits *[]Hit, truncated *bool) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, child := range val {
			subPath := joinPath(path, k)
			if ft := s.matchFieldRule(k); ft != "" {
				if sv, ok := child.(string); ok {
					*hits = append(*hits, Hit{
						Rule:   "field:" + k,
						Type:   ft,
						Start:  0,
						End:    len(sv),
						Masked: maskCredential(sv),
						Field:  subPath,
					})
					if s.action == ActionRedact {
						out[k] = fmt.Sprintf("[REDACTED:%s]", ft)
						continue
					}
					out[k] = sv
					continue
				}
			}
			out[k] = s.walkJSON(child, subPath, hits, truncated)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, child := range val {
			subPath := fmt.Sprintf("%s[%d]", path, i)
			out[i] = s.walkJSON(child, subPath, hits, truncated)
		}
		return out
	case string:
		res := s.scanText(val, path)
		if res.Truncated {
			*truncated = true
		}
		if len(res.Hits) == 0 {
			return val
		}
		*hits = append(*hits, res.Hits...)
		if s.action == ActionRedact {
			return s.RedactText(val, res.Hits)
		}
		return val
	default:
		return val
	}
}

// walkMap is like walkJSON but mutates the map in place (used by ScanJSONMap).
// mutate is true when the caller wants redaction applied directly.
func (s *Scanner) walkMap(m map[string]any, path string, hits *[]Hit, truncated *bool, mutate bool) {
	for k, child := range m {
		subPath := joinPath(path, k)
		if ft := s.matchFieldRule(k); ft != "" {
			if sv, ok := child.(string); ok {
				*hits = append(*hits, Hit{
					Rule:   "field:" + k,
					Type:   ft,
					Start:  0,
					End:    len(sv),
					Masked: maskCredential(sv),
					Field:  subPath,
				})
				if mutate {
					m[k] = fmt.Sprintf("[REDACTED:%s]", ft)
				}
				continue
			}
		}
		switch cv := child.(type) {
		case map[string]any:
			s.walkMap(cv, subPath, hits, truncated, mutate)
		case []any:
			s.walkSlice(cv, subPath, hits, truncated, mutate)
		case string:
			res := s.scanText(cv, subPath)
			if res.Truncated {
				*truncated = true
			}
			if len(res.Hits) == 0 {
				continue
			}
			*hits = append(*hits, res.Hits...)
			if mutate {
				m[k] = s.RedactText(cv, res.Hits)
			}
		}
	}
}

func (s *Scanner) walkSlice(a []any, path string, hits *[]Hit, truncated *bool, mutate bool) {
	for i, child := range a {
		subPath := fmt.Sprintf("%s[%d]", path, i)
		switch cv := child.(type) {
		case map[string]any:
			s.walkMap(cv, subPath, hits, truncated, mutate)
		case []any:
			s.walkSlice(cv, subPath, hits, truncated, mutate)
		case string:
			res := s.scanText(cv, subPath)
			if res.Truncated {
				*truncated = true
			}
			if len(res.Hits) == 0 {
				continue
			}
			*hits = append(*hits, res.Hits...)
			if mutate {
				a[i] = s.RedactText(cv, res.Hits)
			}
		}
	}
}

// matchFieldRule returns the FieldRule.Type that matches key, or "".
func (s *Scanner) matchFieldRule(key string) string {
	for _, fr := range s.fieldRules {
		if fr.Pattern.MatchString(key) {
			return fr.Type
		}
	}

	return ""
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}

	return parent + "." + key
}

// maskCredential returns a preview of a credential: first 4 chars + "****".
// Credentials shorter than 8 chars mask to "****".
func maskCredential(s string) string {
	if len(s) < 8 {
		return "****"
	}

	return s[:4] + "****"
}

// SummarizeTypes returns a sorted, de-duplicated list of hit Types.
func SummarizeTypes(hits []Hit) []string {
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		seen[h.Type] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}

	sort.Strings(out)

	return out
}

// SummarizeMasked returns de-duplicated masked previews (stable order).
func SummarizeMasked(hits []Hit) []string {
	seen := make(map[string]struct{}, len(hits))
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if _, ok := seen[h.Masked]; ok {
			continue
		}
		seen[h.Masked] = struct{}{}
		out = append(out, h.Masked)
	}

	return out
}
