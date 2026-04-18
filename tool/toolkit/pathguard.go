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

package toolkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// PathGuard binds a set of canonicalized allowed directories and provides
// root-based access primitives. It is used by the file tools (read/write/edit)
// to enforce that all filesystem access stays within an allow-listed set of
// directories, with TOCTOU-safe semantics via os.Root (openat2(RESOLVE_BENEATH)
// on Linux, emulation elsewhere).
//
// PathGuard is safe for concurrent use after construction.
type PathGuard struct {
	roots []*rootEntry
}

type rootEntry struct {
	canonical string   // absolute, symlinks resolved
	root      *os.Root // pre-opened
}

// NewPathGuard canonicalizes each dir (abs + EvalSymlinks; must exist), rejects
// filesystem roots ("/" on Unix, drive-roots or UNC roots on Windows),
// deduplicates by containment (subdirectories of another entry are dropped),
// and opens one os.Root per surviving entry.
//
// Passing an empty dirs slice returns a guard with Allowed()==false, meaning
// "no restriction" — preserved for vage library use cases where callers did
// not opt into allow-list enforcement.
func NewPathGuard(dirs []string) (*PathGuard, error) {
	if len(dirs) == 0 {
		return &PathGuard{}, nil
	}

	canonical, err := CanonicalizeDirs(dirs)
	if err != nil {
		return nil, err
	}

	g := &PathGuard{}

	for _, c := range canonical {
		r, openErr := os.OpenRoot(c)
		if openErr != nil {
			_ = g.Close()

			return nil, fmt.Errorf("open root %q: %w", c, openErr)
		}

		g.roots = append(g.roots, &rootEntry{canonical: c, root: r})
	}

	return g, nil
}

// CanonicalizeDirs canonicalizes a set of directories: expands each to an
// absolute path, resolves symlinks, verifies the directory exists, rejects
// filesystem roots, and deduplicates by containment (drops subdirectories of
// another entry). Returns a new sorted slice; does not mutate input.
func CanonicalizeDirs(dirs []string) ([]string, error) {
	out := make([]string, 0, len(dirs))

	for _, d := range dirs {
		if d == "" {
			return nil, errors.New("allowed_dirs: entry must not be empty")
		}

		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("allowed_dirs %q: %w", d, err)
		}

		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("allowed_dirs %q: %w", d, err)
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("allowed_dirs %q: %w", d, err)
		}

		if !info.IsDir() {
			return nil, fmt.Errorf("allowed_dirs %q: not a directory", d)
		}

		if isFilesystemRoot(resolved) {
			return nil, fmt.Errorf("allowed_dirs %q: filesystem root is not permitted", d)
		}

		out = append(out, resolved)
	}

	// Dedupe by containment: sort shortest first, drop any entry that has an
	// ancestor already in the result.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })

	deduped := make([]string, 0, len(out))

	for _, p := range out {
		covered := false

		for _, existing := range deduped {
			if pathContains(existing, p) {
				covered = true

				break
			}
		}

		if !covered {
			deduped = append(deduped, p)
		}
	}

	sort.Strings(deduped)

	return deduped, nil
}

// isFilesystemRoot reports whether p is the root of a filesystem and therefore
// not a safe allow-list entry. On Unix, the root is "/". On Windows, drive
// roots ("C:\") and UNC shares are rejected.
func isFilesystemRoot(p string) bool {
	if p == string(filepath.Separator) {
		return true
	}

	if runtime.GOOS == "windows" {
		// Drive root like "C:\" or "c:/". filepath.VolumeName returns e.g. "C:".
		vol := filepath.VolumeName(p)
		if vol != "" && len(p) == len(vol)+1 && (p[len(vol)] == '\\' || p[len(vol)] == '/') {
			return true
		}
		// UNC root like "\\server\share" with nothing after.
		if strings.HasPrefix(p, `\\`) && strings.Count(p, `\`) <= 3 {
			return true
		}
	}

	return false
}

// pathContains reports whether ancestor is an ancestor of (or equal to) descendant.
// Uses filepath.Rel so it handles platform separators correctly.
func pathContains(ancestor, descendant string) bool {
	if ancestor == descendant {
		return true
	}

	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Allowed reports whether the guard enforces any restriction.
func (g *PathGuard) Allowed() bool {
	return g != nil && len(g.roots) > 0
}

// Dirs returns the canonical allowed directories, sorted. Safe to call on nil.
func (g *PathGuard) Dirs() []string {
	if g == nil {
		return nil
	}

	out := make([]string, len(g.roots))
	for i, r := range g.roots {
		out[i] = r.canonical
	}

	return out
}

// Close closes each pre-opened root. Returns the first error encountered.
// Safe to call on nil.
func (g *PathGuard) Close() error {
	if g == nil {
		return nil
	}

	var firstErr error

	for _, r := range g.roots {
		if r.root != nil {
			if err := r.root.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// Check validates p and returns the canonical absolute path (rooted at the
// matching allowed directory), the path relative to that root, and the root
// itself. On rejection returns a descriptive error suitable for a tool error
// message. The returned abs is stable across literal and symlink-fallback
// matches so callers can use it as a lock/display key.
//
// Check does not require p to exist. For an existing p, symlink escape is
// caught when the caller later invokes a Root.* method (which goes through
// openat2(RESOLVE_BENEATH) on Linux).
func (g *PathGuard) Check(toolName, p string) (abs, rel string, root *os.Root, err error) {
	if g == nil || len(g.roots) == 0 {
		return "", "", nil, fmt.Errorf("%s tool: path guard not configured", toolName)
	}

	if err := checkPathSurface(toolName, p); err != nil {
		return "", "", nil, err
	}

	cleaned := filepath.Clean(p)

	// Resolve symlinks along the path unconditionally. This covers both:
	//  - "/var/..." → "/private/var/..." (allowed root canonicalization), and
	//  - "<allowed>/link → <outside>" (symlink inside allowlist pointing out).
	// os.Root would catch case 2 atomically for read/write/edit, but glob/grep
	// shell out to subprocesses that are not bounded by os.Root, so the guard
	// must reject the escape here before the subprocess ever runs.
	resolved, resolveErr := ResolveExistingPath(cleaned)
	if resolveErr != nil {
		// If we can't resolve (e.g. the path doesn't exist yet and no ancestor
		// resolves either), fall back to the literal cleaned form.
		resolved = cleaned
	}

	if abs, rel, root, ok := g.matchRoot(resolved); ok {
		// If the literal cleaned form also matched but at a different root, or
		// at the same root but with a different rel, it means a symlink
		// redirected the request. That is allowed only when the redirect
		// stays inside some allowed root — which is exactly what matchRoot on
		// the resolved form just confirmed. So success here is fine.
		return abs, rel, root, nil
	}

	// If the literal path was inside an allowed root but the resolved form
	// escaped, the escape happened via a symlink and gets the explicit error.
	if _, _, _, litMatched := g.matchRoot(cleaned); litMatched && resolved != cleaned {
		return "", "", nil, fmt.Errorf("%s tool: symlink resolves outside allowed directories: %s (allowed: %s)",
			toolName, p, formatAllowedDirs(g.Dirs()))
	}

	return "", "", nil, fmt.Errorf("%s tool: path not allowed: %s (allowed: %s)",
		toolName, p, formatAllowedDirs(g.Dirs()))
}

// matchRoot tries each root for containment of p. Returns (abs, rel, root,
// true) on the first match; false otherwise. The returned abs is always
// rooted at the matching canonical directory so callers receive a stable
// display/lock key regardless of whether the input reached matchRoot via
// the literal path or the ResolveExistingPath fallback.
func (g *PathGuard) matchRoot(p string) (string, string, *os.Root, bool) {
	for _, r := range g.roots {
		rel, err := filepath.Rel(r.canonical, p)
		if err != nil {
			continue
		}

		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}

		// Normalize abs to the canonical form so literal and fallback matches
		// agree. When rel == "." the canonical dir itself is the target.
		var abs string
		if rel == "." {
			abs = r.canonical
		} else {
			abs = filepath.Join(r.canonical, rel)
		}

		return abs, filepath.ToSlash(rel), r.root, true
	}

	return "", "", nil, false
}

// checkPathSurface rejects path surfaces that PathGuard never accepts,
// independently of the allow-list membership check: empty, non-absolute,
// UNC, Windows extended-length/device, and drive-relative forms.
func checkPathSurface(toolName, p string) error {
	if p == "" {
		return fmt.Errorf("%s tool: file_path must not be empty", toolName)
	}

	// Reject UNC and Windows extended-path forms universally. UNC paths confuse
	// containment checks on any OS; extended-length/device forms disable
	// filesystem normalization on Windows.
	if strings.HasPrefix(p, `\\`) {
		return fmt.Errorf("%s tool: UNC/extended paths are not allowed: %s", toolName, p)
	}

	if strings.HasPrefix(p, "//") && len(p) > 2 && p[2] != '/' {
		return fmt.Errorf("%s tool: UNC paths are not allowed: %s", toolName, p)
	}

	if !filepath.IsAbs(p) {
		return fmt.Errorf("%s tool: path must be absolute: %s", toolName, p)
	}

	if runtime.GOOS == "windows" {
		// "C:foo" — drive-relative, no separator after the drive letter.
		if len(p) >= 3 && p[1] == ':' && (p[2] != '\\' && p[2] != '/') {
			return fmt.Errorf("%s tool: drive-relative paths are not allowed: %s", toolName, p)
		}
		// 8.3 short-name marker (~N) embedded between separators is suspicious;
		// reject the entire class of "PROGRA~1"-style segments.
		if containsShortNameSegment(p) {
			return fmt.Errorf("%s tool: Windows 8.3 short-name paths are not allowed: %s", toolName, p)
		}
	}

	return nil
}

func containsShortNameSegment(p string) bool {
	for seg := range strings.SplitSeq(p, `\`) {
		for s := range strings.SplitSeq(seg, "/") {
			if looksLikeShortName(s) {
				return true
			}
		}
	}

	return false
}

func looksLikeShortName(s string) bool {
	if idx := strings.Index(s, "~"); idx >= 0 && idx+1 < len(s) {
		tail := s[idx+1:]
		// digit directly after ~ is the typical 8.3 pattern, e.g. "PROGRA~1".
		if len(tail) > 0 && tail[0] >= '0' && tail[0] <= '9' {
			return true
		}
	}

	return false
}

// formatAllowedDirs renders a compact list for error messages. Shows up to 3
// entries, with "... +N more" for the tail.
func formatAllowedDirs(dirs []string) string {
	const maxShown = 3
	if len(dirs) <= maxShown {
		return "[" + strings.Join(dirs, ", ") + "]"
	}

	head := strings.Join(dirs[:maxShown], ", ")

	return fmt.Sprintf("[%s, ... +%d more]", head, len(dirs)-maxShown)
}

// FormatAllowedDirs is the exported variant for tools that want to include the
// list in their own error messages.
func FormatAllowedDirs(dirs []string) string {
	return formatAllowedDirs(dirs)
}

// OpenForRead opens p for reading via the matching root. The returned file is
// bound to the root — symlinks escaping the root are rejected atomically by
// the underlying os.Root.Open call.
func (g *PathGuard) OpenForRead(toolName, p string) (*os.File, string, error) {
	abs, rel, root, err := g.Check(toolName, p)
	if err != nil {
		return nil, "", err
	}

	f, openErr := root.Open(rel)
	if openErr != nil {
		return nil, abs, fmt.Errorf("%s tool: %w", toolName, openErr)
	}

	return f, abs, nil
}

// OpenForWrite opens p for writing via the matching root with the given flags
// and permission.
func (g *PathGuard) OpenForWrite(toolName, p string, flag int, perm fs.FileMode) (*os.File, string, error) {
	abs, rel, root, err := g.Check(toolName, p)
	if err != nil {
		return nil, "", err
	}

	f, openErr := root.OpenFile(rel, flag, perm)
	if openErr != nil {
		return nil, abs, fmt.Errorf("%s tool: %w", toolName, openErr)
	}

	return f, abs, nil
}

// Stat returns FileInfo for p via the matching root.
func (g *PathGuard) Stat(toolName, p string) (fs.FileInfo, string, error) {
	abs, rel, root, err := g.Check(toolName, p)
	if err != nil {
		return nil, "", err
	}

	info, statErr := root.Stat(rel)
	if statErr != nil {
		return nil, abs, statErr
	}

	return info, abs, nil
}

// Lstat returns FileInfo for p via the matching root, without following a
// terminal symlink. Useful when the caller wants to report on a symlink itself.
func (g *PathGuard) Lstat(toolName, p string) (fs.FileInfo, string, error) {
	abs, rel, root, err := g.Check(toolName, p)
	if err != nil {
		return nil, "", err
	}

	info, lstatErr := root.Lstat(rel)
	if lstatErr != nil {
		return nil, abs, lstatErr
	}

	return info, abs, nil
}

// MkdirAll creates the directory p (and missing parents) inside the matching root.
func (g *PathGuard) MkdirAll(toolName, p string, perm fs.FileMode) error {
	_, rel, root, err := g.Check(toolName, p)
	if err != nil {
		return err
	}

	if rel == "" || rel == "." {
		return nil
	}

	if mkErr := root.MkdirAll(rel, perm); mkErr != nil {
		return fmt.Errorf("%s tool: %w", toolName, mkErr)
	}

	return nil
}

// RootFor returns the matching root and relative path for p without opening
// any file. Callers can then invoke arbitrary Root.* methods.
func (g *PathGuard) RootFor(toolName, p string) (root *os.Root, rel string, abs string, err error) {
	abs, rel, root, err = g.Check(toolName, p)

	return
}
