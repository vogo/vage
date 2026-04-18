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

package bash

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// PathGuardian inspects a shell command for path-based escapes relative to a
// set of allowed directories. It classifies each sub-command (reusing the
// classifier's splitter) and returns the worst-case Classification.
//
// PathGuardian is additive to Classifier: callers typically take the higher
// Tier of both.
type PathGuardian struct {
	allowedDirs []string
	workingDir  string
}

// NewPathGuardian returns a guardian bound to canonical allowed directories
// and the bash tool's working directory (used for resolving relative path
// arguments). Passing an empty allowedDirs disables the guardian.
func NewPathGuardian(allowedDirs []string, workingDir string) *PathGuardian {
	if len(allowedDirs) == 0 {
		return nil
	}

	return &PathGuardian{
		allowedDirs: allowedDirs,
		workingDir:  workingDir,
	}
}

// Classify returns the worst-case Classification across a command's
// sub-commands.
func (g *PathGuardian) Classify(command string) Classification {
	if g == nil {
		return Classification{Tier: TierCaution, SubCommand: command}
	}

	subs := splitSubCommands(command)
	if len(subs) == 0 {
		return Classification{Tier: TierCaution, SubCommand: command}
	}

	var best Classification

	initialized := false

	for _, sub := range subs {
		c := g.classifyOne(sub)
		if !initialized || c.Tier > best.Tier {
			best = c
			initialized = true
		}
	}

	return best
}

func (g *PathGuardian) classifyOne(sub string) Classification {
	tokens := tokenize(sub)
	if len(tokens) == 0 {
		return Classification{Tier: TierCaution, SubCommand: sub}
	}

	// cd edge cases come first since the rule is specific.
	if cls := g.checkCd(tokens, sub); cls.Tier != TierCaution {
		return cls
	}

	// Universal token scan: absolute paths must be under allowed; `..`
	// escapes become Dangerous; /proc|/sys|/dev always Blocked.
	for i, t := range tokens {
		if t.kind == tokenRedirectOp {
			continue
		}
		// Skip the command name itself.
		if i == 0 && t.kind == tokenWord {
			continue
		}

		if cls := g.classifyToken(t.value, sub); cls.Tier != TierCaution {
			return cls
		}
	}

	return Classification{Tier: TierCaution, SubCommand: sub}
}

// checkCd applies the cd edge-case matrix from the design.
func (g *PathGuardian) checkCd(tokens []token, sub string) Classification {
	if len(tokens) == 0 || tokens[0].value != "cd" {
		return Classification{Tier: TierCaution, SubCommand: sub}
	}

	if len(tokens) == 1 {
		return Classification{
			Tier:       TierDangerous,
			Rule:       "cd-home-ambiguous",
			Reason:     "cd with no argument changes to $HOME which may be outside allowed dirs",
			SubCommand: sub,
		}
	}

	target := tokens[1].value

	switch {
	case target == "-":
		return Classification{
			Tier:       TierDangerous,
			Rule:       "cd-prev-unknown",
			Reason:     "cd - changes to OLDPWD which cannot be verified against allowed dirs",
			SubCommand: sub,
		}
	case target == "~" || strings.HasPrefix(target, "~/") || target == "$HOME" || strings.HasPrefix(target, "$HOME/"):
		return Classification{
			Tier:       TierDangerous,
			Rule:       "cd-home-ambiguous",
			Reason:     "cd to $HOME may be outside allowed dirs",
			SubCommand: sub,
		}
	case strings.ContainsAny(target, "$`"):
		return Classification{
			Tier:       TierDangerous,
			Rule:       "cd-variable",
			Reason:     "cd target contains shell expansion; target cannot be verified",
			SubCommand: sub,
		}
	case strings.ContainsAny(target, "*?[]"):
		return Classification{
			Tier:       TierDangerous,
			Rule:       "cd-glob",
			Reason:     "cd target contains glob; expansion cannot be verified",
			SubCommand: sub,
		}
	}

	resolved := g.resolveArg(target)

	if !g.underAllowed(resolved) {
		return Classification{
			Tier:       TierBlocked,
			Rule:       "cd-outside-allowed",
			Reason:     fmt.Sprintf("cd target %q resolves outside allowed directories", target),
			SubCommand: sub,
		}
	}

	return Classification{Tier: TierCaution, SubCommand: sub}
}

// classifyToken evaluates a single argument token for path-escape signals.
func (g *PathGuardian) classifyToken(v, sub string) Classification {
	if v == "" {
		return Classification{Tier: TierCaution, SubCommand: sub}
	}

	// Dots-traversal in relative paths.
	if containsDotDotSegment(v) {
		return Classification{
			Tier:       TierDangerous,
			Rule:       "path-traversal-dots",
			Reason:     fmt.Sprintf("argument %q contains '..' escape", v),
			SubCommand: sub,
		}
	}

	// Absolute path.
	if filepath.IsAbs(v) {
		cleaned := filepath.Clean(v)

		if isSystemSensitive(cleaned) {
			return Classification{
				Tier:       TierBlocked,
				Rule:       "system-sensitive-path",
				Reason:     fmt.Sprintf("argument %q is a system-sensitive path (/proc, /sys, /dev)", v),
				SubCommand: sub,
			}
		}

		if !g.underAllowed(cleaned) {
			return Classification{
				Tier:       TierBlocked,
				Rule:       "path-outside-allowed",
				Reason:     fmt.Sprintf("argument %q is outside allowed directories", v),
				SubCommand: sub,
			}
		}
	}

	return Classification{Tier: TierCaution, SubCommand: sub}
}

// resolveArg resolves a target relative to workingDir and cleans it.
func (g *PathGuardian) resolveArg(v string) string {
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}

	if g.workingDir == "" {
		return filepath.Clean(v)
	}

	return filepath.Clean(filepath.Join(g.workingDir, v))
}

// underAllowed reports whether cleaned-abs p lies under any allowed dir.
func (g *PathGuardian) underAllowed(p string) bool {
	if !filepath.IsAbs(p) {
		// Relative paths are presumed to be inside workingDir which is itself
		// an allowed dir in the typical wiring; conservatively accept.
		return true
	}

	for _, dir := range g.allowedDirs {
		if p == dir {
			return true
		}

		if strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// isSystemSensitive flags /proc, /sys, /dev (and descendants) as always
// blocked, independent of allow-list contents.
func isSystemSensitive(p string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/dev"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}

	return false
}

// containsDotDotSegment reports whether p contains a `..` path component.
// Unlike plain strings.Contains it avoids false positives in things like
// `my..file` or `..foo/`.
func containsDotDotSegment(p string) bool {
	return slices.Contains(strings.Split(p, "/"), "..") ||
		slices.Contains(strings.Split(p, `\`), "..")
}

// tokenKind distinguishes a token's role in a sub-command.
type tokenKind int

const (
	tokenWord tokenKind = iota
	tokenRedirectOp
)

type token struct {
	kind  tokenKind
	value string
}

// tokenize splits a sub-command into whitespace-separated tokens. Single- and
// double-quoted content is carried as opaque; `$(...)` and backticks were
// already extracted by the splitter before we see this input. Redirection
// operators are marked so their targets can be classified as ordinary
// arguments.
func tokenize(sub string) []token {
	var out []token

	var cur strings.Builder

	flushWord := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, token{kind: tokenWord, value: cur.String()})
		cur.Reset()
	}

	emitOp := func(op string) {
		flushWord()
		out = append(out, token{kind: tokenRedirectOp, value: op})
	}

	i := 0
	for i < len(sub) {
		c := sub[i]

		switch {
		case c == ' ' || c == '\t':
			flushWord()
			i++
		case c == '\'':
			// Drop quoted content; word remains non-empty if we were mid-token.
			i++
			for i < len(sub) && sub[i] != '\'' {
				i++
			}
			if i < len(sub) {
				i++
			}
		case c == '"':
			i++
			for i < len(sub) && sub[i] != '"' {
				if sub[i] == '\\' && i+1 < len(sub) {
					i += 2
					continue
				}
				i++
			}
			if i < len(sub) {
				i++
			}
		case c == '>' || c == '<':
			// Redirection operators: >, >>, <, <>, 2>, &>.
			op := string(c)
			if c == '>' && i+1 < len(sub) && sub[i+1] == '>' {
				op = ">>"
				i++
			}
			if c == '<' && i+1 < len(sub) && sub[i+1] == '>' {
				op = "<>"
				i++
			}
			emitOp(op)
			i++
		case c == '\\' && i+1 < len(sub):
			cur.WriteByte(sub[i+1])
			i += 2
		default:
			cur.WriteByte(c)
			i++
		}
	}

	flushWord()

	return out
}
