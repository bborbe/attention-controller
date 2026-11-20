// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
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
	".credentials.json": {}, ".teamvault.json": {}, ".claude.json": {},
}

// routineDenyBasenamePattern matches credential shapes by SHAPE rather than by
// a name list that is always one entry short: `.env` alone misses `.env.local`
// and `.env.production`, and `credentials` alone misses `credentials.json`.
//
// `.env.*` covers every `.env` variant (`.envrc` included) and `id_[^.]*` every
// SSH key name (`id_rsa`, `id_ed25519`, a glob `id_*`) without catching a source
// file such as `id_test.go`.
var routineDenyBasenamePattern = regexp.MustCompile(
	`^(\.env.*|secrets(\..+)?|credentials(\..+)?|id_[^.]*` +
		`|.*\.(pem|key|p12|pfx|tfstate))$`,
)

// routineDenyHomePrefixes are never routine even inside the workspace, because
// these are never a workspace.
var routineDenyHomePrefixes = []string{
	"/.ssh", "/.aws", "/.gnupg", "/.config", "/.kube",
	"/.docker", "/.azure", "/.password-store", "/Library/Keychains", "/.claude",
}

// routineBashSystemCWDs are never a Bash workspace: a child whose cwd is a
// shared scratch root or the system configuration tree is not working in a
// directory it owns. The scratch roots are refused only as the cwd itself — a
// per-job directory under them is a workspace — while the configuration tree is
// refused at any depth.
var (
	routineBashScratchCWDs = []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}
	routineBashConfigCWDs  = []string{"/etc", "/private/etc"}
)

// routineBashPieceSeparators split a rebuilt shell word into the pieces a path
// can hide in: `--file=~/.netrc`, a `PATH`-style list, a brace list.
var routineBashPieceSeparators = regexp.MustCompile(`[=:,{}]+`)

// routineBashWordBreaks end a shell word outside quotes.
const routineBashWordBreaks = ";|&<>()`"

// routineBashGlobChars are the glob metacharacters a word may carry.
const routineBashGlobChars = "*?["

// The guard's bounds. ⚠️ Every one fails CLOSED: the patterns are a headless
// child's text and the guard runs on the settle cycle, so a pattern that would
// cost more than these is held rather than walked.
const (
	// routineBashWordCap is the most words the guard checks in one command.
	routineBashWordCap = 4096
	// routineBashGlobSegmentCap is the most wildcard segments one glob may have.
	routineBashGlobSegmentCap = 4
	// routineBashGlobMatchCap is the most paths one glob may reach.
	routineBashGlobMatchCap = 256
	// routineBashGlobScanCap is the most directory entries one glob may read.
	routineBashGlobScanCap = 4096
)

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

//counterfeiter:generate -o ../mocks/routine-item-policy.go --fake-name RoutineItemPolicy . RoutineItemPolicy

// RoutineItemPolicy is the policy shape the settle arm prefers when the policy
// offers it: it knows which item a gate belongs to and receives the cycle's
// context.
//
// ⚠️ It exists for the Bash rule, whose verdict costs a classifier process. The
// item lets a verdict be reused across settle cycles instead of starting a
// process every two seconds, and the context bounds that process. A policy that
// only implements RoutinePolicy keeps working unchanged.
type RoutineItemPolicy interface {
	RoutinePolicy
	// RoutineRuleForItem returns the rule name and whether the watcher may answer
	// item's gate.
	RoutineRuleForItem(
		ctx context.Context,
		item ItemID,
		question json.RawMessage,
		cwd string,
	) (string, bool)
	// RetainItems drops every cached verdict whose item is not in open.
	RetainItems(open map[ItemID]struct{})
}

// RoutinePolicyOption customises the policy at construction.
type RoutinePolicyOption func(*routinePolicy)

// WithRoutineBashClassifier enables the Bash rule: a `Bash` gate whose command
// the classifier rates `allowlist` is routine. A nil classifier leaves the rule
// disabled.
//
// currentDateTimeGetter is the clock the verdict cache expires on.
func WithRoutineBashClassifier(
	classifier RoutineBashClassifier,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) RoutinePolicyOption {
	return func(policy *routinePolicy) {
		policy.classifier = classifier
		policy.currentDateTimeGetter = currentDateTimeGetter
	}
}

// NewRoutinePolicy creates the policy.
//
// ⚠️ roots are the OPERATOR-declared roots the arm may read beyond the child's
// own cwd. Empty is the default and means "the child's workspace only". A
// declared root that is not a boundary (an empty value, `/`, the home tree, or
// an ancestor of the home tree) is dropped rather than honoured: `/` would make
// every absolute path routine, and `~` would make every path under the home tree
// routine apart from the credential prefixes.
func NewRoutinePolicy(roots []string, options ...RoutinePolicyOption) RoutinePolicy {
	safe := make([]string, 0, len(roots))
	for _, root := range roots {
		if resolved := safeRoot(root); resolved != "" {
			safe = append(safe, resolved)
		}
	}
	policy := &routinePolicy{roots: safe, verdicts: map[ItemID]routineBashVerdict{}}
	for _, option := range options {
		option(policy)
	}
	if policy.currentDateTimeGetter == nil {
		policy.classifier = nil
	}
	return policy
}

type routinePolicy struct {
	roots                 []string
	classifier            RoutineBashClassifier
	currentDateTimeGetter libtime.CurrentDateTimeGetter

	mutex    sync.Mutex
	verdicts map[ItemID]routineBashVerdict
}

// routineBashVerdict is one item's cached Bash decision.
type routineBashVerdict struct {
	command    string
	rule       string
	allowed    bool
	recordedAt libtime.DateTime
}

// routineBashErrorTTL is how long a `tier-error` verdict stands. ⚠️ Short, not
// the hold interval: a classifier that failed mid `make update` is fixed within
// seconds, and the gate it held should not wait five minutes to be retried.
const routineBashErrorTTL = 10 * time.Second

// RoutineRuleForItem returns the rule name and whether the watcher may answer
// item's gate.
//
// ⚠️ The Bash rule runs AHEAD of the path-argument check: a Bash gate carries
// no path argument, so the check would refuse it as `no-routine-rule` before
// the tool was ever looked at. With the classifier disabled the gate falls
// through to that check and is refused exactly as before.
func (p *routinePolicy) RoutineRuleForItem(
	ctx context.Context,
	item ItemID,
	question json.RawMessage,
	cwd string,
) (string, bool) {
	if p.classifier != nil {
		if command, sandboxDisabled, ok := routineBashCommand(question); ok {
			if sandboxDisabled {
				return "bash-sandbox-disabled", false
			}
			return p.bashRule(ctx, item, command, cwd)
		}
	}
	return p.RoutineRule(question, cwd)
}

// RetainItems drops every cached verdict whose item is not in open.
func (p *routinePolicy) RetainItems(open map[ItemID]struct{}) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	for item := range p.verdicts {
		if _, ok := open[item]; !ok {
			delete(p.verdicts, item)
		}
	}
}

// bashRule decides a Bash gate by the classifier's tier.
//
// ⚠️ The cwd boundary and the credential-path guard are checked BEFORE the
// classifier: a child spawned in `/` or the home tree is not a workspace, and a
// command naming a credential location is never routine whatever its tier — the
// classifier's own allowlist rates `cat ~/.claude/.credentials.json` as a plain
// `cat`.
func (p *routinePolicy) bashRule(
	ctx context.Context,
	item ItemID,
	command string,
	cwd string,
) (string, bool) {
	root, ok := bashWorkspace(cwd)
	if !ok {
		return "bash-cwd-not-workspace", false
	}
	now := p.currentDateTimeGetter.Now()
	if rule, allowed, hit := p.cachedVerdict(item, command, now); hit {
		return rule, allowed
	}
	// ⚠️ The guard's verdict is cached beside the classifier's: the guard reads
	// the disk for a glob, and the settle cycle asks again every two seconds.
	rule, allowed := "bash-denied-path", false
	if !bashNamesDeniedPath(ctx, command, root) {
		rule, allowed = p.classify(ctx, command)
	}
	p.mutex.Lock()
	p.verdicts[item] = routineBashVerdict{
		command:    command,
		rule:       rule,
		allowed:    allowed,
		recordedAt: now,
	}
	p.mutex.Unlock()
	return rule, allowed
}

// cachedVerdict returns item's verdict when it is for the same command and
// younger than RoutineGateHoldRepeatInterval (routineBashErrorTTL for a
// `tier-error`).
func (p *routinePolicy) cachedVerdict(
	item ItemID,
	command string,
	now libtime.DateTime,
) (string, bool, bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	verdict, ok := p.verdicts[item]
	if !ok || verdict.command != command {
		return "", false, false
	}
	ttl := RoutineGateHoldRepeatInterval
	if verdict.rule == "tier-error" {
		ttl = routineBashErrorTTL
	}
	if !now.Before(verdict.recordedAt.Add(libtime.Duration(ttl))) {
		return "", false, false
	}
	return verdict.rule, verdict.allowed, true
}

// classify maps the classifier's tier to a rule. Only `allowlist` allows.
func (p *routinePolicy) classify(ctx context.Context, command string) (string, bool) {
	tier, err := p.classifier.Classify(ctx, command)
	if err != nil {
		// ⚠️ The error names the failure class only, never the command.
		glog.Warningf("job=attention-watcher action=routine_bash_classifier_error error=%v", err)
		return "tier-error", false
	}
	switch tier {
	case RoutineBashTierAllowlist:
		return "tier-allowlist", true
	case RoutineBashTierManager:
		return "tier-manager", false
	case RoutineBashTierOperator:
		return "tier-operator", false
	default:
		return "tier-error", false
	}
}

// routineBashCommand returns a Bash gate's non-empty command and whether the
// gate asks to run outside the sandbox.
//
// ⚠️ Any `dangerouslyDisableSandbox` other than an explicit false counts as
// disabled: a non-boolean is not a value this arm can prove harmless.
func routineBashCommand(question json.RawMessage) (string, bool, bool) {
	args, ok := routineArguments(question)
	if !ok {
		return "", false, false
	}
	if tool, ok := args["tool_name"].(string); !ok || tool != "Bash" {
		return "", false, false
	}
	call, ok := args["input"].(map[string]any)
	if !ok {
		return "", false, false
	}
	command, ok := call["command"].(string)
	if !ok || command == "" {
		return "", false, false
	}
	sandboxDisabled := false
	if value, present := call["dangerouslyDisableSandbox"]; present {
		if flag, isBool := value.(bool); !isBool || flag {
			sandboxDisabled = true
		}
	}
	return command, sandboxDisabled, true
}

// bashWorkspace returns the resolved cwd when it is a Bash workspace: absolute,
// a boundary, outside every credential location, and not a system directory.
func bashWorkspace(cwd string) (string, bool) {
	if !absoluteCWD(cwd) {
		return "", false
	}
	root, ok := resolveRealPath(cwd, cwd)
	if !ok || !isBoundary(root) {
		return "", false
	}
	if deniedPath(root) {
		return "", false
	}
	for _, scratch := range routineBashScratchCWDs {
		if root == scratch {
			return "", false
		}
	}
	for _, config := range routineBashConfigCWDs {
		if root == config || strings.HasPrefix(root, config+string(os.PathSeparator)) {
			return "", false
		}
	}
	return root, true
}

// bashNamesDeniedPath reports whether any word of command resolves to a
// credential location under the Read rule's own denylist.
//
// ⚠️ Deliberately over-inclusive: every word is tried as a path, `$HOME` is
// expanded, a relative word is joined with the cwd, and a glob word is expanded
// so `~/.ss*` is judged by what it reaches. A false hold costs one operator
// card; a false allow hands a credential to a headless child.
func bashNamesDeniedPath(ctx context.Context, command string, cwd string) bool {
	home, _ := os.UserHomeDir()
	words, ok := bashWords(ctx, command, home)
	if !ok || len(words) > routineBashWordCap {
		return true
	}
	for _, word := range words {
		if bashWordDenied(word, cwd, home) {
			return true
		}
		for _, piece := range routineBashPieceSeparators.Split(word, -1) {
			if piece != "" && piece != word && bashWordDenied(piece, cwd, home) {
				return true
			}
		}
	}
	return false
}

// bashWords rebuilds command's shell words: adjacent quoted and unquoted pieces
// are joined, backslash escapes are removed, and `$HOME` / `${HOME}` outside
// single quotes is expanded. It returns false for an unterminated quote.
//
// ⚠️ It is the guard's own reading of the command, so the guard does not lean
// on the classifier refusing quotes and `$`: `.e"nv"` is the word `.env`.
func bashWords(ctx context.Context, command string, home string) ([]string, bool) {
	runes := []rune(command)
	words := make([]string, 0, 8)
	var word strings.Builder
	inWord := false
	flush := func() bool {
		// ⚠️ A cancelled cycle stops the scan and holds the gate.
		if ctx.Err() != nil {
			return false
		}
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
		return true
	}
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		switch {
		case r == '\\':
			if index+1 < len(runes) {
				index++
				word.WriteRune(runes[index])
			}
			inWord = true
		case r == '\'' || r == '"':
			end, ok := bashQuoted(runes, index, home, &word)
			if !ok {
				return nil, false
			}
			index = end
			inWord = true
		case r == '$':
			index += bashHomeVariable(runes[index:], home, &word) - 1
			inWord = true
		case unicode.IsSpace(r) || strings.ContainsRune(routineBashWordBreaks, r):
			if !flush() {
				return nil, false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if !flush() {
		return nil, false
	}
	return words, true
}

// bashQuoted copies the quoted span opening at runes[start] into word and
// returns the index of its closing quote. A single-quoted span is literal.
func bashQuoted(runes []rune, start int, home string, word *strings.Builder) (int, bool) {
	if runes[start] == '"' {
		return bashDoubleQuoted(runes, start+1, home, word)
	}
	for end := start + 1; end < len(runes); end++ {
		if runes[end] == '\'' {
			word.WriteString(string(runes[start+1 : end]))
			return end, true
		}
	}
	return 0, false
}

// bashDoubleQuoted copies a double-quoted span starting at start into word and
// returns the index of its closing quote.
func bashDoubleQuoted(runes []rune, start int, home string, word *strings.Builder) (int, bool) {
	for index := start; index < len(runes); index++ {
		switch runes[index] {
		case '"':
			return index, true
		case '\\':
			if index+1 < len(runes) && strings.ContainsRune("\"\\$`", runes[index+1]) {
				index++
			}
			word.WriteRune(runes[index])
		case '$':
			index += bashHomeVariable(runes[index:], home, word) - 1
		default:
			word.WriteRune(runes[index])
		}
	}
	return 0, false
}

// bashHomeVariable writes `$HOME` / `${HOME}` at the start of runes as home and
// returns how many runes it consumed; any other `$` is copied as itself.
func bashHomeVariable(runes []rune, home string, word *strings.Builder) int {
	if consumed := bashHomeVariableLength(runes); home != "" && consumed > 0 {
		word.WriteString(home)
		return consumed
	}
	word.WriteRune('$')
	return 1
}

// bashHomeVariableLength returns the length of a `${HOME}` or `$HOME` at the
// start of runes, or 0 — `$HOMEDIR` is a different variable.
//
// ⚠️ It reads a bounded prefix, never the rest of the command: it runs on every
// `$`, so converting the whole tail would make the scan quadratic.
func bashHomeVariableLength(runes []rune) int {
	text := string(runes[:min(len(runes), len("${HOME}"))])
	if strings.HasPrefix(text, "${HOME}") {
		return len("${HOME}")
	}
	if !strings.HasPrefix(text, "$HOME") {
		return 0
	}
	rest := runes[len("$HOME"):]
	if len(rest) > 0 && (unicode.IsLetter(rest[0]) || unicode.IsDigit(rest[0]) || rest[0] == '_') {
		return 0
	}
	return len("$HOME")
}

// bashWordDenied reports whether one command word, read as a path, reaches a
// credential location — literally, through a symlink, as a directory above
// one, or through a glob.
func bashWordDenied(word string, cwd string, home string) bool {
	// ⚠️ `~user`, `~+` and `~-` name a directory the guard cannot resolve without
	// a user lookup; it fails closed instead.
	if strings.HasPrefix(word, "~") && word != "~" && !strings.HasPrefix(word, "~/") {
		return true
	}
	path := expandHome(word)
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if deniedPath(path) || coversDeniedLocation(path, home) {
		return true
	}
	if real, ok := resolveRealPath(path, cwd); ok &&
		(deniedPath(real) || coversDeniedLocation(real, home)) {
		return true
	}
	if !strings.ContainsAny(word, routineBashGlobChars) {
		return false
	}
	matches, ok := boundedGlob(path)
	if !ok {
		return true
	}
	for _, match := range matches {
		if deniedPath(match) || coversDeniedLocation(match, home) {
			return true
		}
	}
	return false
}

// coversDeniedLocation reports whether path is the home tree, an ancestor of
// it, or a directory with a credential location beneath it — a word a
// recursive `grep` or `find` reads every credential through.
func coversDeniedLocation(path string, home string) bool {
	if path == string(os.PathSeparator) {
		return true
	}
	if home == "" {
		return false
	}
	under := path + string(os.PathSeparator)
	if path == home || strings.HasPrefix(home, under) {
		return true
	}
	for _, prefix := range routineDenyHomePrefixes {
		if strings.HasPrefix(home+prefix, under) {
			return true
		}
	}
	return false
}

// boundedGlob expands an absolute, clean pattern segment by segment, or reports
// false when it exceeds a guard bound or is malformed.
//
// ⚠️ Not filepath.Glob: that walks a child-supplied pattern with no bound, and
// `/*/*/*/*/*/*/*/*` walks the whole disk on every settle cycle.
func boundedGlob(pattern string) ([]string, bool) {
	segments := strings.Split(
		strings.TrimPrefix(pattern, string(os.PathSeparator)),
		string(os.PathSeparator),
	)
	wildcards := 0
	for _, segment := range segments {
		if strings.ContainsAny(segment, routineBashGlobChars) {
			if _, err := filepath.Match(segment, ""); err != nil {
				return nil, false
			}
			wildcards++
		}
	}
	if wildcards > routineBashGlobSegmentCap {
		return nil, false
	}
	current := []string{string(os.PathSeparator)}
	scanned := 0
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		next := make([]string, 0, len(current))
		for _, dir := range current {
			if !strings.ContainsAny(segment, routineBashGlobChars) {
				next = append(next, filepath.Join(dir, segment))
				continue
			}
			matched, read, ok := globDirectory(dir, segment, routineBashGlobScanCap-scanned)
			scanned += read
			if !ok {
				return nil, false
			}
			next = append(next, matched...)
		}
		if len(next) > routineBashGlobMatchCap {
			return nil, false
		}
		current = next
	}
	return current, true
}

// globDirectory returns dir's entries matching segment, reading at most budget
// entries; it reports false when the budget runs out.
func globDirectory(dir string, segment string, budget int) ([]string, int, bool) {
	handle, err := os.Open(dir) // #nosec G304 -- read for names only, never content
	if err != nil {
		return nil, 0, true
	}
	defer handle.Close()
	matched := make([]string, 0, 8)
	read := 0
	for {
		entries, err := handle.ReadDir(256)
		for _, entry := range entries {
			read++
			if read > budget {
				return nil, read, false
			}
			if ok, _ := filepath.Match(segment, entry.Name()); ok {
				matched = append(matched, filepath.Join(dir, entry.Name()))
			}
		}
		if err != nil || len(entries) == 0 {
			return matched, read, true
		}
	}
}

// RoutineRule returns the rule name and whether the watcher may answer.
//
// ⚠️ It never runs the Bash rule: that rule starts a process, and this entry
// point has neither a context to bound it nor an item to cache it under. A Bash
// gate decided here is refused as `no-routine-rule`; the settle arm reaches the
// Bash rule through RoutineRuleForItem.
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

// routineArguments returns the question's tool name and tool input as one map.
//
// ⚠️ The wrapper publishes a FLAT question — `{"tool_name": "Bash", "input":
// {...}}` — and that is the shape every live gate carries. This function once
// read only `input.arguments`, a nesting no producer sends: every gate failed
// the parse and was refused as `no-routine-rule` before the tool was ever
// looked at, so the item-aware Bash rule could never be reached. The nested
// form stays accepted because an older wrapper sent it and this arm's older
// fixtures carry it; a payload matching neither fails closed.
//
// ⚠️ The shape checks are isinstance-guarded, not just `.get`-ed. A payload
// whose `input` or `arguments` is a truthy non-object would panic inside the
// settle loop and abort the whole poll, skipping every later arm.
//
// ⚠️ Flat is selected by the KEY'S PRESENCE, not by a non-empty value: a
// question that carries `tool_name` at all is a flat one, and an empty value is
// malformed rather than an invitation to read `input.arguments`. `input` is
// model-controlled tool input, so letting an empty `tool_name` fall through to
// the legacy branch would let a tool input's own `arguments` key choose the
// tool name the policy sees.
func routineArguments(question json.RawMessage) (map[string]any, bool) {
	var envelope struct {
		ToolName *string        `json:"tool_name"`
		Input    map[string]any `json:"input"`
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
	// Flat: the wrapper's own question object, the shape every live gate carries.
	if envelope.ToolName != nil {
		if *envelope.ToolName == "" {
			return nil, false
		}
		return map[string]any{"tool_name": *envelope.ToolName, "input": envelope.Input}, true
	}
	// Nested: `{"input": {"arguments": {"tool_name": ..., "input": {...}}}}`.
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
var _ RoutineItemPolicy = (*routinePolicy)(nil)
