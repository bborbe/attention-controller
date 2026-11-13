// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// routineReadTools are the FILE-scoped tools the watcher may settle, and only
// inside a root the operator declared or inside the session's own workspace.
//
// ⚠️ The tool NAME alone is not a policy, and treating it as one is the defect
// this shape exists to prevent: `Read` reaches `~/.ssh/id_rsa` exactly as easily
// as a source file, and a headless child can be induced — by a file or a page it
// reads — to ask for one. A routine gate is therefore a routine tool whose own
// path arguments resolve INSIDE the workspace and OUTSIDE the credential
// locations below.
//
// ⚠️ FILE-scoped tools only. `Grep` and `Glob` take a DIRECTORY, and the files
// they then read under it are never seen by the credential check — so a Grep of
// a workspace containing a `.env` would be routine while reading the secret. A
// tool whose reads cannot be enumerated is not one this arm may decide.
var routineReadTools = map[string]struct{}{"Read": {}, "NotebookRead": {}}

// routineWriteTools are the write-class tools the watcher may settle, and ONLY
// inside a root the operator declared — never inside the child's own cwd, which
// the child did not choose and the operator did not name.
var routineWriteTools = map[string]struct{}{"Write": {}, "Edit": {}, "NotebookEdit": {}}

// routinePathArgs are the tool-input keys that carry a path.
var routinePathArgs = []string{"file_path", "path", "notebook_path"}

// routineDenyBasenames are never routine wherever they sit — a workspace can
// legitimately contain a `.env`.
var routineDenyBasenames = map[string]struct{}{
	".netrc": {}, ".npmrc": {}, ".pypirc": {}, ".git-credentials": {}, "kubeconfig": {},
}

// routineDenyBasenamePattern matches credential shapes by SHAPE rather than by
// a name list that is always one entry short: `.env` alone misses `.env.local`
// and `.env.production`, and `credentials` alone misses `credentials.json`.
var routineDenyBasenamePattern = regexp.MustCompile(
	`^(\.env(\..+)?|secrets(\..+)?|credentials(\..+)?|id_[a-z0-9]+` +
		`|.*\.(pem|key|p12|pfx|tfstate))$`,
)

// routineDenyHomePrefixes are never routine even inside the workspace, because
// these are never a workspace.
var routineDenyHomePrefixes = []string{
	"/.ssh", "/.aws", "/.gnupg", "/.config", "/.kube",
	"/.docker", "/.azure", "/.password-store",
}

// routineTokenPattern reduces a gate-supplied tool name to a token safe for a
// store field and a log line.
var routineTokenPattern = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// RoutinePolicy decides whether the watcher may answer a gate without the
// operator.
//
// It is an interface so a spec can drive the settle arm without a real home
// tree.
type RoutinePolicy interface {
	// RoutineRule returns the rule name and whether the watcher may answer.
	//
	// The name is recorded either way — an allowed gate names the rule that
	// admitted it, a refused one names the rule that declined it — so the record
	// distinguishes "a policy decided" from "a constant was stamped".
	RoutineRule(question json.RawMessage, cwd string) (string, bool)
}

// NewRoutinePolicy creates the policy.
//
// ⚠️ roots are the OPERATOR-declared roots the arm may read beyond the child's
// own cwd. Empty is the default and means "the child's workspace only". A
// declared root that is not a boundary (an empty value, `/`, the home tree, or
// an ancestor of the home tree) is dropped rather than honoured: `/` would make
// every absolute path routine, and `~` would make every path under the home tree
// routine apart from the credential prefixes.
func NewRoutinePolicy(roots []string) RoutinePolicy {
	safe := make([]string, 0, len(roots))
	for _, root := range roots {
		if resolved := safeRoot(root); resolved != "" {
			safe = append(safe, resolved)
		}
	}
	return &routinePolicy{roots: safe}
}

type routinePolicy struct {
	roots []string
}

// RoutineRule returns the rule name and whether the watcher may answer.
//
// ⚠️ Two questions, not one. The tool name says what KIND of action it is; the
// path arguments say whether THIS call is the routine kind. A gate with no path
// argument at all is refused rather than assumed to read the cwd, because the
// assumption is the whole risk.
func (p *routinePolicy) RoutineRule(question json.RawMessage, cwd string) (string, bool) {
	args, ok := routineArguments(question)
	if !ok {
		return "no-routine-rule", false
	}
	tool, ok := args["tool_name"].(string)
	if !ok || tool == "" {
		return "no-routine-rule", false
	}
	call, ok := args["input"].(map[string]any)
	if !ok {
		return "no-routine-rule", false
	}
	paths := routinePaths(call)
	if len(paths) == 0 {
		return "no-routine-rule", false
	}
	token := routineToken(tool)
	if _, isRead := routineReadTools[tool]; isRead {
		for _, path := range paths {
			if !p.pathAllowed(path, cwd) {
				return "path-outside-workspace:" + token, false
			}
		}
		return "routine-tool:" + token, true
	}
	if _, isWrite := routineWriteTools[tool]; isWrite {
		for _, path := range paths {
			if !p.underDeclaredRoot(path, cwd) {
				return "write-outside-declared-root:" + token, false
			}
		}
		return "routine-write:" + token, true
	}
	return "no-routine-rule", false
}

// routineArguments returns the question's `input.arguments` object.
//
// ⚠️ Both are isinstance-guarded, not just `.get`-ed. A payload whose `input`
// or `arguments` is a truthy non-object would panic inside the settle loop and
// abort the whole poll, skipping every later arm.
func routineArguments(question json.RawMessage) (map[string]any, bool) {
	var envelope struct {
		Input map[string]any `json:"input"`
	}
	if len(question) == 0 {
		return nil, false
	}
	if err := json.Unmarshal(question, &envelope); err != nil {
		return nil, false
	}
	if envelope.Input == nil {
		return nil, false
	}
	args, ok := envelope.Input["arguments"].(map[string]any)
	return args, ok
}

// routinePaths returns the non-empty string path arguments.
func routinePaths(call map[string]any) []string {
	paths := make([]string, 0, len(routinePathArgs))
	for _, key := range routinePathArgs {
		if value, ok := call[key].(string); ok && value != "" {
			paths = append(paths, value)
		}
	}
	return paths
}

// pathAllowed reports whether path resolves inside the session's workspace and
// is not a credential location.
func (p *routinePolicy) pathAllowed(raw string, cwd string) bool {
	if !absoluteCWD(cwd) {
		return false
	}
	real, ok := resolveRealPath(raw, cwd)
	if !ok {
		return false
	}
	root, ok := resolveRealPath(cwd, cwd)
	if !ok {
		return false
	}
	// ⚠️ The session cwd is a workspace only when it is a boundary at all — the
	// same test safeRoot applies to a declared root, ancestors included. A child
	// spawned with cwd `/`, `~` or `/Users` would otherwise make nearly every
	// read routine, and the cwd is not operator-chosen.
	if !isBoundary(root) {
		return false
	}
	if deniedPath(real) {
		return false
	}
	if real == root || strings.HasPrefix(real, root+string(os.PathSeparator)) {
		return true
	}
	for _, declared := range p.roots {
		if real == declared || strings.HasPrefix(real, declared+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// underDeclaredRoot reports whether path resolves inside a root the operator
// declared routine.
func (p *routinePolicy) underDeclaredRoot(raw string, cwd string) bool {
	if !absoluteCWD(cwd) {
		return false
	}
	real, ok := resolveRealPath(raw, cwd)
	if !ok {
		return false
	}
	if deniedPath(real) {
		return false
	}
	for _, declared := range p.roots {
		if real == declared || strings.HasPrefix(real, declared+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// absoluteCWD reports whether cwd is present and absolute.
//
// ⚠️ cwd must be ABSOLUTE as well as present. A relative one would be joined
// and resolved against the WATCHER's cwd, so the workspace boundary would
// silently become the watcher's directory — a fail-open against this file's own
// "anything unprovable is refused" contract.
func absoluteCWD(cwd string) bool {
	return cwd != "" && filepath.IsAbs(cwd)
}

// safeRoot returns a declared routine root, or "" when the declaration is not a
// boundary.
//
// ⚠️ An EMPTY value returns "", and that guard is load-bearing rather than
// defensive: resolving "" is the CURRENT DIRECTORY, so without it an unset root
// list — the documented default — would silently make the watcher's own cwd a
// declared routine root.
func safeRoot(raw string) string {
	if raw == "" {
		return ""
	}
	real, ok := resolveRealPath(expandHome(raw), "")
	if !ok {
		return ""
	}
	if !isBoundary(real) {
		return ""
	}
	return real
}

// isBoundary reports whether path is a usable workspace boundary: not `/`, not
// the home tree, and not an ancestor of it.
func isBoundary(path string) bool {
	if path == "" || path == string(os.PathSeparator) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true
	}
	if home == path {
		return false
	}
	return !strings.HasPrefix(home, path+string(os.PathSeparator))
}

// resolveRealPath resolves path to an absolute, symlink-free form.
//
// ⚠️ It is NOT plain filepath.Abs: a relative tool-input path resolved against
// the WATCHER's cwd names a different file than the one the child will touch —
// so the policy would decide about one path and the child act on another. That
// is a wrong decision, not a wrong log line.
func resolveRealPath(raw string, cwd string) (string, bool) {
	if raw == "" {
		return "", false
	}
	expanded := expandHome(raw)
	if !filepath.IsAbs(expanded) {
		if cwd == "" {
			return "", false
		}
		expanded = filepath.Join(cwd, expanded)
	}
	real, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		// A path that does not exist has no real form. Resolve its DIRECTORY
		// and re-join the base, so a not-yet-created write target is still
		// evaluated against the same symlink-resolved boundary as its parent —
		// otherwise a temp dir reached through a symlink (macOS /var) would
		// compare a resolved root against an unresolved child and refuse a
		// path that is genuinely inside it.
		if resolvedDir, dirErr := filepath.EvalSymlinks(filepath.Dir(expanded)); dirErr == nil {
			return filepath.Join(resolvedDir, filepath.Base(expanded)), true
		}
		real = filepath.Clean(expanded)
	}
	return real, true
}

// expandHome expands a leading `~`.
func expandHome(raw string) string {
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(raw, "~"))
		}
	}
	return raw
}

// deniedPath reports whether an absolute, resolved path names a credential
// location.
func deniedPath(real string) bool {
	base := filepath.Base(real)
	if _, denied := routineDenyBasenames[base]; denied {
		return true
	}
	if routineDenyBasenamePattern.MatchString(base) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	for _, prefix := range routineDenyHomePrefixes {
		if real == home+prefix || strings.HasPrefix(real, home+prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// routineToken reduces a gate-supplied tool name to a token safe for a store
// field and a log line.
//
// ⚠️ The refusal arms interpolate a GATE-SUPPLIED name into the attempt carrier
// and the log line, so it is reduced first: a newline would forge a log line,
// and an unbounded value would land in the store's `carrier` field.
func routineToken(tool string) string {
	token := routineTokenPattern.ReplaceAllString(tool, "")
	if len(token) > 40 {
		token = token[:40]
	}
	if token == "" {
		return "unknown"
	}
	return token
}

// Compile-time guard: the policy must satisfy its own interface.
var _ RoutinePolicy = (*routinePolicy)(nil)
