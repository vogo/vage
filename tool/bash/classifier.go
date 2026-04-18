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
	"regexp"
	"strings"
)

// Tier classifies how a bash command should be gated.
type Tier int

const (
	// TierSafe: no confirmation required.
	TierSafe Tier = iota
	// TierCaution: default tier for unmatched commands; prompt with standard three-state dialog.
	TierCaution
	// TierDangerous: prompt per-invocation; the caller should not offer an "allow always" option.
	TierDangerous
	// TierBlocked: hard reject; never execute regardless of permission mode.
	TierBlocked
)

// String returns a stable lowercase identifier for the tier.
func (t Tier) String() string {
	switch t {
	case TierSafe:
		return "safe"
	case TierCaution:
		return "caution"
	case TierDangerous:
		return "dangerous"
	case TierBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// Rule is one entry in the classifier's rule library.
type Rule struct {
	Name    string
	Tier    Tier
	Pattern *regexp.Regexp
	Reason  string
}

// Classification is the outcome for a single command.
type Classification struct {
	Tier       Tier
	Rule       string // matched rule name; empty when Tier == TierCaution and nothing matched
	Reason     string
	SubCommand string // the sub-command that determined the tier
}

// Classifier matches a command string against a rule library and returns the
// highest-tier match across all sub-commands.
type Classifier struct {
	rules []Rule
}

// NewClassifier returns a Classifier over the given rules. Rules are evaluated
// in order; on ties the first matching rule of the highest tier wins.
func NewClassifier(rules []Rule) *Classifier {
	return &Classifier{rules: rules}
}

// Classify returns the worst-case classification across the command's
// sub-commands (split on `;`, `&&`, `||`, and extracted from `$(...)` / backticks).
func (c *Classifier) Classify(command string) Classification {
	subs := splitSubCommands(command)
	if len(subs) == 0 {
		return Classification{Tier: TierCaution, SubCommand: command}
	}

	var best Classification

	initialized := false

	for _, sub := range subs {
		m := c.classifyOne(sub)
		if !initialized || m.Tier > best.Tier {
			best = m
			initialized = true
		}
	}

	return best
}

func (c *Classifier) classifyOne(sub string) Classification {
	var matched *Rule

	for i := range c.rules {
		r := &c.rules[i]
		if r.Pattern.MatchString(sub) {
			if matched == nil || r.Tier > matched.Tier {
				matched = r
			}
		}
	}

	if matched != nil {
		return Classification{
			Tier:       matched.Tier,
			Rule:       matched.Name,
			Reason:     matched.Reason,
			SubCommand: sub,
		}
	}

	return Classification{Tier: TierCaution, SubCommand: sub}
}

// splitSubCommands decomposes a shell command into the list of sub-commands
// the classifier should evaluate. Decomposition rules:
//
//   - Top-level separators `;`, `&&`, `||`, `&` start a new sub-command.
//   - Pipes (`|`) stay inside the current sub-command so patterns like
//     "curl ... | bash" can match the pipeline as a whole.
//   - Single-quoted content is dropped (it cannot be expanded or executed).
//   - Double-quoted content is dropped, but any `$(...)` or `` ` `` subshells
//     inside it are extracted and classified recursively.
//   - Bare `$(...)` and backticks are extracted as their own sub-commands.
//
// The goal is approximate correctness for classification, not a full shell
// parser. Known limitation: variable-value resolution is not performed, so
// `X=rm; $X -rf /` is not caught by the destructive-rm rule.
func splitSubCommands(cmd string) []string {
	var out []string

	var cur strings.Builder

	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			out = append(out, s)
		}

		cur.Reset()
	}

	i := 0
	for i < len(cmd) {
		c := cmd[i]

		switch {
		case c == '\\' && i+1 < len(cmd):
			cur.WriteByte(c)
			cur.WriteByte(cmd[i+1])
			i += 2
		case c == '\'':
			i++
			for i < len(cmd) && cmd[i] != '\'' {
				i++
			}

			if i < len(cmd) {
				i++
			}
		case c == '"':
			i++
			for i < len(cmd) && cmd[i] != '"' {
				switch {
				case cmd[i] == '\\' && i+1 < len(cmd):
					i += 2
				case cmd[i] == '`':
					inner, next := readBackticks(cmd, i)
					out = append(out, splitSubCommands(inner)...)
					i = next
				case cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
					inner, next := readParen(cmd, i+1)
					out = append(out, splitSubCommands(inner)...)
					i = next
				default:
					i++
				}
			}

			if i < len(cmd) {
				i++
			}
		case c == '`':
			inner, next := readBackticks(cmd, i)
			out = append(out, splitSubCommands(inner)...)
			i = next
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
			inner, next := readParen(cmd, i+1)
			out = append(out, splitSubCommands(inner)...)
			i = next
		case c == ';':
			flush()
			i++
		case c == '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				flush()
				i += 2
			} else {
				cur.WriteByte(c)
				i++
			}
		case c == '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				flush()
				i += 2
			} else {
				cur.WriteByte(c)
				i++
			}
		default:
			cur.WriteByte(c)
			i++
		}
	}

	flush()

	return out
}

// readBackticks reads `...` starting at cmd[i] == '`' and returns the inner
// text plus the index after the closing backtick.
func readBackticks(cmd string, i int) (string, int) {
	var b strings.Builder

	j := i + 1
	for j < len(cmd) {
		if cmd[j] == '\\' && j+1 < len(cmd) {
			b.WriteByte(cmd[j+1])
			j += 2

			continue
		}

		if cmd[j] == '`' {
			return b.String(), j + 1
		}

		b.WriteByte(cmd[j])
		j++
	}

	return b.String(), j
}

// readParen reads (...) starting at cmd[i] == '(' and returns the inner text
// plus the index after the matching close paren. Single- and double-quoted
// segments are skipped so parentheses inside strings don't affect depth.
func readParen(cmd string, i int) (string, int) {
	depth := 1
	j := i + 1
	start := j

	for j < len(cmd) && depth > 0 {
		c := cmd[j]

		switch {
		case c == '\\' && j+1 < len(cmd):
			j += 2
		case c == '\'':
			j++
			for j < len(cmd) && cmd[j] != '\'' {
				j++
			}

			if j < len(cmd) {
				j++
			}
		case c == '"':
			j++
			for j < len(cmd) && cmd[j] != '"' {
				if cmd[j] == '\\' && j+1 < len(cmd) {
					j += 2

					continue
				}

				j++
			}

			if j < len(cmd) {
				j++
			}
		case c == '(':
			depth++
			j++
		case c == ')':
			depth--
			j++
		default:
			j++
		}
	}

	end := j
	inner := ""

	if end-1 >= start {
		inner = cmd[start : end-1]
	}

	return inner, end
}

// DefaultRules returns the hard-coded baseline rule library. Callers typically
// combine these with user-configured extensions.
//
// The list is intentionally short and conservative: only well-known,
// high-confidence patterns are blocked or marked dangerous, and the safe list
// covers the most common read-only operations in this project's workflow.
func DefaultRules() []Rule {
	r := func(name string, tier Tier, pattern, reason string) Rule {
		return Rule{
			Name:    name,
			Tier:    tier,
			Pattern: regexp.MustCompile(pattern),
			Reason:  reason,
		}
	}

	return []Rule{
		// Blocked: never execute.
		r("destructive-rm-root", TierBlocked,
			`\brm\s+-[a-zA-Z]*[rRf][a-zA-Z]*\s+/(\s|$|--|;|&|\|)`,
			"recursive delete of root filesystem"),
		r("privilege-escalation-sudo", TierBlocked,
			`(^|[\s|;&])sudo(\s|$)`,
			"privilege escalation via sudo"),
		r("privilege-escalation-su", TierBlocked,
			`(^|[\s|;&])su\s+-`,
			"privilege escalation via su -"),
		r("privilege-escalation-doas", TierBlocked,
			`(^|[\s|;&])doas(\s|$)`,
			"privilege escalation via doas"),
		r("dd-to-device", TierBlocked,
			`\bdd\b[^;&|]*\bof=/dev/`,
			"raw write to block device"),
		r("mkfs", TierBlocked,
			`\bmkfs(\.[a-z0-9]+)?\b`,
			"filesystem format"),
		r("fork-bomb", TierBlocked,
			`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}`,
			"fork bomb pattern"),
		r("write-to-block-device", TierBlocked,
			`>\s*/dev/[sh]d[a-z]`,
			"redirect to block device"),

		// Dangerous: prompt per-invocation, no allow-always.
		r("docker-rm-force", TierDangerous,
			`\bdocker\s+rm\s+-f\b`,
			"force-removes docker containers"),
		r("recursive-rm", TierDangerous,
			`\brm\s+-[a-zA-Z]*[rRf][a-zA-Z]*\b`,
			"recursive or forced delete"),
		r("shred", TierDangerous,
			`\bshred\b`,
			"secure file deletion"),
		r("curl-to-shell", TierDangerous,
			`\bcurl\b[^;&]*\|\s*(sh|bash|zsh|ksh)\b`,
			"piping remote content into a shell"),
		r("wget-to-shell", TierDangerous,
			`\bwget\b[^;&]*\|\s*(sh|bash|zsh|ksh)\b`,
			"piping remote content into a shell"),
		r("shell-process-substitution-remote", TierDangerous,
			`\b(bash|sh|zsh|ksh)\s+<\s*\(\s*(curl|wget)\b`,
			"executing remote content via process substitution"),
		r("pip-install-url", TierDangerous,
			`\bpip\s+install\s+https?://`,
			"pip install from arbitrary URL"),
		r("git-push-force", TierDangerous,
			`\bgit\s+push\s+.*(--force\b|-f\b|--force-with-lease\b)`,
			"force push rewrites remote history"),
		r("git-reset-hard", TierDangerous,
			`\bgit\s+reset\s+--hard\b`,
			"hard reset discards working changes"),
		r("git-clean-force", TierDangerous,
			`\bgit\s+clean\s+-[a-zA-Z]*f`,
			"force clean deletes untracked files"),
		r("git-branch-delete-force", TierDangerous,
			`\bgit\s+branch\s+-D\b`,
			"force delete of unmerged branch"),
		r("git-filter-branch", TierDangerous,
			`\bgit\s+filter-branch\b`,
			"rewrites repository history"),
		r("eval", TierDangerous,
			`(^|[\s|;&])eval(\s|$)`,
			"dynamic code evaluation"),
		r("shell-dash-c", TierDangerous,
			`\b(sh|bash|zsh|ksh)\s+-c\b`,
			"indirect shell invocation"),
		r("kubectl-delete", TierDangerous,
			`\bkubectl\s+delete\b`,
			"deletes Kubernetes resources"),
		r("docker-prune", TierDangerous,
			`\bdocker\s+system\s+prune\b`,
			"reclaims unused docker data"),
		r("chmod-world-writable", TierDangerous,
			`\bchmod\s+-*[Rr]?\s*7[0-7][0-7]\b`,
			"world-writable permissions"),
		r("iptables-flush", TierDangerous,
			`\biptables\s+-[FX]\b`,
			"flushes firewall rules"),
		r("read-ssh-keys", TierDangerous,
			`\bcat\b[^;&|]*(\~|\$HOME|/home/[^/\s]+|/root)/\.ssh/`,
			"reading SSH private keys"),
		r("read-aws-credentials", TierDangerous,
			`\bcat\b[^;&|]*(\~|\$HOME|/home/[^/\s]+|/root)/\.aws/credentials`,
			"reading AWS credentials"),
		r("read-netrc", TierDangerous,
			`\bcat\b[^;&|]*(\~|\$HOME|/home/[^/\s]+|/root)/\.netrc`,
			"reading .netrc credentials"),
		r("read-shadow", TierDangerous,
			`\bcat\b[^;&|]*/etc/shadow\b`,
			"reading /etc/shadow"),
		r("env-dump", TierDangerous,
			`^\s*(env|printenv)\s*$`,
			"full environment dump may leak secrets"),

		// Safe: bypass confirmation dialog.
		r("safe-ls", TierSafe, `^ls(\s|$)`, "directory listing"),
		r("safe-pwd", TierSafe, `^pwd\s*$`, "print working directory"),
		r("safe-echo", TierSafe, `^echo(\s|$)`, "echo"),
		r("safe-date", TierSafe, `^date(\s|$)`, "date"),
		r("safe-whoami", TierSafe, `^whoami\s*$`, "user name"),
		r("safe-id", TierSafe, `^id\s*$`, "user id"),
		r("safe-uname", TierSafe, `^uname(\s|$)`, "system info"),
		r("safe-hostname", TierSafe, `^hostname\s*$`, "host name"),
		r("safe-head", TierSafe, `^head\s`, "head of file"),
		r("safe-tail", TierSafe, `^tail\s`, "tail of file"),
		r("safe-wc", TierSafe, `^wc\s`, "word count"),
		r("safe-file", TierSafe, `^file\s`, "file type"),
		r("safe-stat", TierSafe, `^stat\s`, "file stat"),
		r("safe-which", TierSafe, `^which\s`, "executable lookup"),
		r("safe-ps", TierSafe, `^ps(\s|$)`, "process list"),
		r("safe-grep", TierSafe, `^grep\s`, "grep"),
		r("safe-rg", TierSafe, `^rg\s`, "ripgrep"),
		r("safe-go-readonly", TierSafe,
			`^go\s+(build|test|vet|fmt|doc|env|version|list|mod\s+(tidy|download|verify|why|graph))\b`,
			"go toolchain read/build"),
		r("safe-git-readonly", TierSafe,
			`^git\s+(status|diff|log|show|branch|remote|fetch|pull|stash\s+list|config\s+--get|rev-parse|ls-files|ls-tree)\b`,
			"git read operations"),
		r("safe-make", TierSafe,
			`^make\s+(build|test|lint|format|license-check|vet|check)\b`,
			"make build targets"),
	}
}
