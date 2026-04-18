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
	"testing"
)

func TestPathGuardian_Nil(t *testing.T) {
	var g *PathGuardian

	c := g.Classify("echo hi")
	if c.Tier != TierCaution {
		t.Errorf("nil guardian should return Caution, got %v", c.Tier)
	}
}

func TestPathGuardian_Empty(t *testing.T) {
	g := NewPathGuardian(nil, "/tmp")
	if g != nil {
		t.Error("expected nil guardian for empty allowed")
	}
}

func TestPathGuardian_CdInside(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cd /work/sub")
	if c.Tier == TierBlocked || c.Tier == TierDangerous {
		t.Errorf("cd inside should not be blocked/dangerous, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_CdOutside(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cd /etc")
	if c.Tier != TierBlocked {
		t.Errorf("cd outside should be Blocked, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_CdHomeAmbiguous(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	for _, cmd := range []string{"cd", "cd ~", "cd $HOME", "cd ~/foo"} {
		c := g.Classify(cmd)
		if c.Tier != TierDangerous {
			t.Errorf("%s expected Dangerous, got %v rule=%s", cmd, c.Tier, c.Rule)
		}
	}
}

func TestPathGuardian_CdPrev(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cd -")
	if c.Tier != TierDangerous {
		t.Errorf("cd - expected Dangerous, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_CdVariable(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cd $SOMEVAR")
	if c.Tier != TierDangerous {
		t.Errorf("cd $VAR expected Dangerous, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_CdGlob(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cd /work/*")
	if c.Tier != TierDangerous {
		t.Errorf("cd glob expected Dangerous, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_AbsolutePathOutside(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cat /etc/passwd")
	if c.Tier != TierBlocked {
		t.Errorf("expected Blocked for absolute outside, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_SystemSensitive(t *testing.T) {
	// Explicitly allow /proc, /dev, /sys so we verify the unconditional block
	// fires regardless of allow-list membership.
	g := &PathGuardian{allowedDirs: []string{"/proc", "/dev", "/sys", "/work"}, workingDir: "/work"}

	for _, p := range []string{"cat /proc/1/environ", "cat /dev/sda", "echo > /sys/kernel/foo"} {
		c := g.Classify(p)
		if c.Tier != TierBlocked {
			t.Errorf("%s expected Blocked for system-sensitive, got %v rule=%s", p, c.Tier, c.Rule)
		}
	}
}

func TestPathGuardian_RedirectOutside(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("echo hi > /etc/motd")
	if c.Tier != TierBlocked {
		t.Errorf("redirect outside expected Blocked, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_DotDot(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("cat ../../../etc/passwd")
	if c.Tier != TierDangerous {
		t.Errorf("expected Dangerous for dot-dot escape, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_CommandSubstitutionExtracted(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	c := g.Classify("echo $(cat /etc/passwd)")
	if c.Tier != TierBlocked {
		t.Errorf("subshell should have extracted cat /etc/passwd as Blocked, got %v rule=%s", c.Tier, c.Rule)
	}
}

func TestPathGuardian_HappyPath(t *testing.T) {
	g := NewPathGuardian([]string{"/work"}, "/work")

	for _, cmd := range []string{
		"echo hello",
		"ls /work",
		"cat /work/file.txt",
		"go test ./...",
	} {
		c := g.Classify(cmd)
		if c.Tier == TierBlocked || c.Tier == TierDangerous {
			t.Errorf("%s should not be blocked/dangerous, got %v rule=%s", cmd, c.Tier, c.Rule)
		}
	}
}
