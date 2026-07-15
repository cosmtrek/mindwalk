package adapter

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/model"
)

type Source interface {
	Harness() string
	SessionDir() string
	ListSessions() ([]model.SessionMeta, error)
	Summarize(path string) (model.SessionMeta, error)
	Parse(path string) (*model.Trace, error)
}

type ToolCall struct {
	ID        string
	Name      string
	Input     map[string]any
	Timestamp string
}

type ToolResult struct {
	Content string
	IsError bool
}

// SessionKey identifies one session file independently of the harness-level
// session ID. Codex resume rollouts can share an ID across multiple files, so
// IDs are display metadata rather than safe routing or cache keys.
func SessionKey(harness, path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	sum := sha256.Sum256([]byte(harness + "\x00" + path))
	return fmt.Sprintf("%s-%x", harness, sum[:12])
}

// userMessageNoteLimit bounds the text stored on user-message marks; the note
// exists so downstream analysis can see the task wording, not to mirror the
// full transcript.
const userMessageNoteLimit = 2000

// UserMessageNote normalizes user-message text for Mark.Note: trimmed and
// truncated to a fixed rune budget, ellipsis marker included — the schema
// promises the note never exceeds userMessageNoteLimit runes in total.
func UserMessageNote(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= userMessageNoteLimit {
		return text
	}
	return string(runes[:userMessageNoteLimit-1]) + "…"
}

// InjectedUserMessage recognizes harness-injected text recorded as a user
// message but not written by the user. Adapters drop these before they
// become user-message marks — they would inflate user-turn stats, clutter
// the timeline, and pose as the task in judge input.
//
// Injected wrappers observed in real logs (<system-reminder>,
// <command-name>, <local-command-caveat>, <environment_context>,
// <recommended_plugins>, <turn_aborted>, …) are complete markup envelopes:
// the message starts with a tag and ends with one. That shape — rather than
// a tag whitelist that new harness versions would outgrow, or a bare "<"
// prefix that would swallow real tasks pasting HTML/JSX followed by a
// question — is what marks a message as injected. Codex's AGENTS.md
// instructions are the one non-markup injection.
func InjectedUserMessage(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "# AGENTS.md instructions") {
		return true
	}
	return strings.HasPrefix(text, "<") && strings.HasSuffix(text, ">")
}

func ReadJSONLines(r io.Reader, visit func([]byte)) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				visit(line)
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func BuildEvent(trace *model.Trace, call ToolCall, result ToolResult) model.Event {
	action := actionFor(call.Name, call.Input, result.Content)
	targets, outside := targetsFor(trace.Session.Cwd, call.Name, call.Input, result.Content)
	if targets == nil {
		targets = []model.Target{}
	}
	return model.Event{
		Seq:         len(trace.Events),
		Timestamp:   call.Timestamp,
		Tool:        call.Name,
		Action:      action,
		Targets:     targets,
		Outside:     outside,
		ResultBytes: len(result.Content),
		IsError:     result.IsError,
		Summary:     SummarizeTool(call.Name, call.Input, targets, outside, result.IsError),
	}
}

func ContentToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
				if text, ok := m["content"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func IntFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}

func actionFor(tool string, input map[string]any, result string) string {
	switch tool {
	case "Read":
		return "read"
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch":
		return "edit"
	case "Grep", "Glob", "LS", "view_image":
		return "search"
	case "Bash", "exec_command", "write_stdin", "js", "js_repl":
		command := firstString(input, "command", "cmd", "code", "chars", "script", "_raw")
		if verifyCommand(command) {
			return "verify"
		}
		if searchCommand(command) {
			return "search"
		}
		if readCommand(command) {
			return "read"
		}
		return "exec"
	case "exec":
		if len(execPatchPaths(input)) > 0 {
			return "edit"
		}
		commands := execCommands(input)
		if len(commands) == 0 || !execHasOnlyStaticCommands(input, len(commands)) {
			return "exec"
		}
		allVerify, allSearch, allRead := true, true, true
		for _, command := range commands {
			if !verifyCommand(command.command) {
				allVerify = false
			}
			if !searchCommand(command.command) {
				allSearch = false
			}
			if !readCommand(command.command) {
				allRead = false
			}
		}
		if allVerify {
			return "verify"
		}
		if allSearch {
			return "search"
		}
		if allRead {
			return "read"
		}
		return "exec"
	default:
		_ = result
		return "other"
	}
}

func targetsFor(cwd, tool string, input map[string]any, result string) ([]model.Target, []model.OutsideTouch) {
	var targets []model.Target
	var outside []model.OutsideTouch
	add := func(path, touch string, weak bool, lines [][2]int, base string) {
		rel, out, ok := normalizePath(cwd, base, path)
		if !ok {
			return
		}
		if out != nil {
			outside = append(outside, *out)
			return
		}
		if weak && !repoPathExists(cwd, rel) {
			return
		}
		for i := range targets {
			if targets[i].Path == rel {
				if model.RankTouch(touch) > model.RankTouch(targets[i].Touch) {
					targets[i].Touch = touch
				}
				targets[i].Lines = append(targets[i].Lines, lines...)
				return
			}
		}
		targets = append(targets, model.Target{Path: rel, Touch: touch, Lines: lines, Weak: weak})
	}

	switch tool {
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			add(path, "read", false, readLines(input), "")
		}
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		if path, ok := input["file_path"].(string); ok {
			add(path, "edit", false, nil, "")
		}
		if path, ok := input["notebook_path"].(string); ok {
			add(path, "edit", false, nil, "")
		}
	case "Grep":
		for _, hit := range parsePathHits(result) {
			add(hit.path, "hit", false, hit.lines, "")
		}
		if len(targets) == 0 {
			if path, ok := input["path"].(string); ok {
				add(path, "hit", true, nil, "")
			}
		}
	case "Glob", "LS":
		for _, hit := range parsePathHits(result) {
			add(hit.path, "hit", false, nil, "")
		}
		if path, ok := input["path"].(string); ok && len(targets) == 0 {
			add(path, "hit", true, nil, "")
		}
	case "Bash":
		command := firstString(input, "command")
		for _, path := range commandReadPaths(command) {
			add(path, "read", true, nil, "")
		}
		for _, path := range extractCommandPaths(command) {
			add(path, "hit", true, nil, "")
		}
		for _, path := range extractPaths(command + "\n" + result) {
			add(path, "hit", true, nil, "")
		}
	case "exec_command":
		command := firstString(input, "cmd", "command")
		base := firstString(input, "workdir")
		for _, path := range commandReadPaths(command) {
			add(path, "read", true, nil, base)
		}
		for _, path := range extractCommandPaths(command) {
			add(path, "hit", true, nil, base)
		}
		for _, path := range extractPaths(command + "\n" + result) {
			add(path, "hit", true, nil, base)
		}
		for _, hit := range parsePathHits(result) {
			add(hit.path, "hit", true, hit.lines, base)
		}
	case "exec":
		for _, command := range execCommands(input) {
			for _, path := range commandReadPaths(command.command) {
				add(path, "read", true, nil, command.workdir)
			}
			for _, path := range extractCommandPaths(command.command) {
				add(path, "hit", true, nil, command.workdir)
			}
			for _, path := range extractPaths(command.command) {
				add(path, "hit", true, nil, command.workdir)
			}
		}
		// The aggregate result does not retain which inner command produced each
		// line, so resolve result-only paths against the session cwd rather than
		// guessing a workdir.
		for _, path := range extractPaths(result) {
			add(path, "hit", true, nil, "")
		}
		for _, hit := range parsePathHits(result) {
			add(hit.path, "hit", true, hit.lines, "")
		}
		for _, path := range execPatchPaths(input) {
			add(path, "edit", false, nil, "")
		}
	case "apply_patch":
		patch := firstString(input, "patch", "input", "_raw")
		for _, path := range parsePatchPaths(patch) {
			add(path, "edit", false, nil, "")
		}
	case "view_image":
		if path := firstString(input, "path"); path != "" {
			add(path, "read", false, nil, "")
		}
	case "js", "js_repl":
		code := firstString(input, "code", "script", "_raw")
		for _, path := range extractPaths(code + "\n" + result) {
			add(path, "hit", true, nil, "")
		}
	}
	return targets, outside
}

func firstString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := input[key].(string); ok {
			return value
		}
	}
	return ""
}

type execCommand struct {
	command string
	workdir string
}

// execStringFieldRe recognizes only JSON-compatible double-quoted string
// literals assigned to cmd or workdir. It intentionally does not attempt to
// parse arbitrary JavaScript expressions.
var execStringFieldRe = regexp.MustCompile(`(?:^|[[:space:],{])(?:"(cmd|workdir)"|(cmd|workdir))\s*:\s*("(?:\\.|[^"\\])*")`)
var execPatchAssignmentRe = regexp.MustCompile(`(?m)^[\t ]*(?:const|let|var)[\t ]+patch[\t ]*=[\t ]*("(?:\\.|[^"\\])*")[\t ]*;`)

func execSource(input map[string]any) string {
	for _, key := range []string{"_raw", "code", "script"} {
		if candidate, ok := input[key].(string); ok && candidate != "" {
			return candidate
		}
	}
	return ""
}

func execHasOnlyStaticCommands(input map[string]any, commandCount int) bool {
	source := execSource(input)
	if source == "" {
		return firstString(input, "cmd", "command") != ""
	}
	tools := execToolNames(source)
	if len(tools) != commandCount {
		return false
	}
	for _, tool := range tools {
		if tool != "exec_command" {
			return false
		}
	}
	return true
}

func execCommands(input map[string]any) []execCommand {
	source := execSource(input)
	if source == "" {
		if command := firstString(input, "cmd", "command"); command != "" {
			return []execCommand{{command: command, workdir: firstString(input, "workdir")}}
		}
		return nil
	}

	arguments := execCommandArguments(source)
	commands := make([]execCommand, 0, len(arguments))
	for _, argument := range arguments {
		if command, ok := parseStaticExecCommand(argument); ok {
			commands = append(commands, command)
		}
	}
	return commands
}

func execPatchPaths(input map[string]any) []string {
	source := execSource(input)
	if source == "" {
		return nil
	}
	match := execPatchAssignmentRe.FindStringSubmatch(source)
	if len(match) != 2 {
		return nil
	}
	var patch string
	if json.Unmarshal([]byte(match[1]), &patch) != nil {
		return nil
	}
	for _, argument := range execToolArguments(source, "apply_patch") {
		if strings.TrimSpace(argument) == "patch" {
			return parsePatchPaths(patch)
		}
	}
	return nil
}

func parseStaticExecCommand(argument string) (execCommand, bool) {
	var command execCommand
	ambiguousWorkdir := false
	for _, match := range execStringFieldRe.FindAllStringSubmatchIndex(argument, -1) {
		keyStart, keyEnd := match[2], match[3]
		if keyStart < 0 {
			keyStart, keyEnd = match[4], match[5]
		}
		key := argument[keyStart:keyEnd]

		var value string
		if err := json.Unmarshal([]byte(argument[match[6]:match[7]]), &value); err != nil {
			continue
		}

		if key == "cmd" {
			if command.command != "" {
				return execCommand{}, false
			}
			command.command = value
			continue
		}

		if command.workdir != "" {
			ambiguousWorkdir = true
			command.workdir = ""
			continue
		}
		if !ambiguousWorkdir {
			command.workdir = value
		}
	}
	return command, command.command != ""
}

func execCommandArguments(source string) []string {
	return execToolArguments(source, "exec_command")
}

func execToolArguments(source, tool string) []string {
	call := "tools." + tool
	var arguments []string
	for i := 0; i < len(source); {
		if next, ok := skipJSIgnored(source, i); ok {
			i = next
			continue
		}
		if !strings.HasPrefix(source[i:], call) || (i > 0 && isJSIdentifierByte(source[i-1])) {
			i++
			continue
		}
		open := i + len(call)
		for open < len(source) && isJSSpace(source[open]) {
			open++
		}
		if open >= len(source) || source[open] != '(' {
			i++
			continue
		}
		close, ok := matchingJSParen(source, open)
		if !ok {
			break
		}
		arguments = append(arguments, source[open+1:close])
		i = close + 1
	}
	return arguments
}

func execToolNames(source string) []string {
	const prefix = "tools."
	var names []string
	for i := 0; i < len(source); {
		if next, ok := skipJSIgnored(source, i); ok {
			i = next
			continue
		}
		if !strings.HasPrefix(source[i:], prefix) || (i > 0 && isJSIdentifierByte(source[i-1])) {
			i++
			continue
		}
		nameStart := i + len(prefix)
		nameEnd := nameStart
		for nameEnd < len(source) && isJSIdentifierByte(source[nameEnd]) {
			nameEnd++
		}
		open := nameEnd
		for open < len(source) && isJSSpace(source[open]) {
			open++
		}
		if nameEnd == nameStart || open >= len(source) || source[open] != '(' {
			i++
			continue
		}
		names = append(names, source[nameStart:nameEnd])
		i = open + 1
	}
	return names
}

func matchingJSParen(source string, open int) (int, bool) {
	depth := 1
	for i := open + 1; i < len(source); {
		if next, ok := skipJSIgnored(source, i); ok {
			i = next
			continue
		}
		switch source[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
		i++
	}
	return 0, false
}

func skipJSIgnored(source string, start int) (int, bool) {
	if start >= len(source) {
		return start, false
	}
	if quote := source[start]; quote == '\'' || quote == '"' || quote == '`' {
		for i := start + 1; i < len(source); i++ {
			if source[i] == '\\' {
				i++
				continue
			}
			if source[i] == quote {
				return i + 1, true
			}
		}
		return len(source), true
	}
	if source[start] != '/' || start+1 >= len(source) {
		return start, false
	}
	switch source[start+1] {
	case '/':
		if end := strings.IndexByte(source[start+2:], '\n'); end >= 0 {
			return start + 2 + end + 1, true
		}
		return len(source), true
	case '*':
		if end := strings.Index(source[start+2:], "*/"); end >= 0 {
			return start + 2 + end + 2, true
		}
		return len(source), true
	default:
		return start, false
	}
}

func isJSSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func isJSIdentifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func repoPathExists(cwd, rel string) bool {
	if cwd == "" || rel == "" {
		return false
	}
	abs := filepath.Join(cwd, filepath.FromSlash(rel))
	_, err := os.Stat(abs)
	return err == nil
}

func readLines(input map[string]any) [][2]int {
	offset := IntFromAny(input["offset"])
	limit := IntFromAny(input["limit"])
	if offset <= 0 {
		return nil
	}
	if limit <= 0 {
		return [][2]int{{offset, offset}}
	}
	return [][2]int{{offset, offset + limit - 1}}
}

func normalizePath(cwd, base, path string) (string, *model.OutsideTouch, bool) {
	path = strings.TrimSpace(strings.Trim(path, `"'`))
	if path == "" || strings.Contains(path, "\n") {
		return "", nil, false
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return "", nil, false
	}
	if !filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		if clean == "." || strings.HasPrefix(clean, "..") {
			return "", nil, false
		}
		if base != "" && filepath.IsAbs(base) {
			abs := filepath.Clean(filepath.Join(base, clean))
			if cwd != "" {
				root := filepath.Clean(cwd)
				if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
					return filepath.ToSlash(rel), nil, true
				}
			}
			return "", &model.OutsideTouch{Scope: outsideScope(abs), Path: abs}, true
		}
		return filepath.ToSlash(clean), nil, true
	}
	abs := filepath.Clean(path)
	if cwd != "" {
		root := filepath.Clean(cwd)
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return filepath.ToSlash(rel), nil, true
		}
	}
	return "", &model.OutsideTouch{Scope: outsideScope(abs), Path: abs}, true
}

func outsideScope(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return "home"
		}
	}
	if strings.HasPrefix(path, os.TempDir()) || strings.HasPrefix(path, "/tmp") {
		return "tmp"
	}
	return "other"
}

type pathHit struct {
	path  string
	lines [][2]int
}

var pathLineRe = regexp.MustCompile(`(?:^|[\s"'([])([A-Za-z0-9_./@+-]*[A-Za-z0-9_/@+-]\.[A-Za-z0-9][A-Za-z0-9._-]*):([0-9]+)`)
var pathOnlyRe = regexp.MustCompile(`(?:^|[\s"'([])([./~A-Za-z0-9_@+-]*[/][A-Za-z0-9_./~@+-]*\.[A-Za-z0-9][A-Za-z0-9._-]*)(?:$|[\s"',)\]:;])`)
var commandPathRe = regexp.MustCompile(`(?:^|[\s"'=])([./~A-Za-z0-9_@+-]+\.[A-Za-z0-9][A-Za-z0-9._-]*)(?:$|[\s"',)\]:;])`)
var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)

func parsePathHits(text string) []pathHit {
	byPath := map[string][][2]int{}
	for _, m := range pathLineRe.FindAllStringSubmatch(text, -1) {
		line := 0
		fmt.Sscanf(m[2], "%d", &line)
		if line > 0 {
			if path, ok := cleanExtractedPath(m[1], true); ok {
				byPath[path] = append(byPath[path], [2]int{line, line})
			}
		}
	}
	for _, p := range extractPaths(text) {
		if _, ok := byPath[p]; !ok {
			byPath[p] = nil
		}
	}
	out := make([]pathHit, 0, len(byPath))
	for path, lines := range byPath {
		out = append(out, pathHit{path: path, lines: lines})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func extractPaths(text string) []string {
	matches := pathOnlyRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		path, ok := cleanExtractedPath(m[1], false)
		if !ok {
			continue
		}
		if path == "" || seen[path] || strings.Contains(path, "://") {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func extractCommandPaths(command string) []string {
	matches := commandPathRe.FindAllStringSubmatch(command, -1)
	seen := map[string]bool{}
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		path, ok := cleanExtractedPath(m[1], true)
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func parsePatchPaths(patch string) []string {
	matches := patchFileRe.FindAllStringSubmatch(patch, -1)
	seen := map[string]bool{}
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		path, ok := cleanExtractedPath(raw, true)
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func cleanExtractedPath(path string, allowTopLevel bool) (string, bool) {
	path = strings.TrimSpace(strings.Trim(path, `"' ,;:()[]{}`))
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	path = strings.TrimPrefix(path, "./")
	if path == "" || strings.Contains(path, "://") || strings.ContainsAny(path, "\n\r\t") {
		return "", false
	}
	if strings.HasPrefix(path, "--") || strings.HasPrefix(path, "++") {
		return "", false
	}
	if !allowTopLevel && !strings.Contains(path, "/") {
		return "", false
	}
	return path, true
}

var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

var searchPrograms = map[string]bool{
	"grep": true, "rg": true, "ag": true, "find": true, "fd": true,
	"ls": true, "tree": true,
}

var readOnlyPrograms = map[string]bool{
	"cd": true, "cat": true, "head": true, "tail": true, "wc": true,
	"sort": true, "uniq": true, "cut": true, "awk": true, "echo": true,
	"which": true, "file": true, "stat": true, "du": true, "pwd": true,
	"dirname": true, "basename": true, "true": true,
}

// searchCommand reports whether a shell command only inspects the tree the
// way Grep/Glob/LS would: every pipeline segment runs a read-only program and
// at least one segment actually searches or lists. Conservative by design —
// anything unrecognized stays "exec".
func searchCommand(command string) bool {
	cleaned := strings.NewReplacer("2>&1", " ", "2>/dev/null", " ", ">/dev/null", " ", "> /dev/null", " ").Replace(command)
	searched := false
	segments := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '|' || r == ';' || r == '&' || r == '\n'
	})
	for _, segment := range segments {
		if strings.ContainsRune(segment, '>') {
			return false
		}
		fields := strings.Fields(segment)
		for len(fields) > 0 && envAssignRe.MatchString(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		program := strings.ToLower(filepath.Base(fields[0]))
		if program == "git" && len(fields) > 1 && (fields[1] == "grep" || fields[1] == "ls-files") {
			searched = true
			continue
		}
		if searchPrograms[program] {
			// find can mutate through -exec/-delete; keep those out of "search"
			if strings.Contains(segment, "-exec") || strings.Contains(segment, "-delete") {
				return false
			}
			searched = true
			continue
		}
		if !readOnlyPrograms[program] {
			return false
		}
	}
	return searched
}

var readPrograms = map[string]bool{
	"cat": true, "head": true, "tail": true, "nl": true, "sed": true,
}

// readCommand reports whether a shell command only pages file contents the
// way Read would: every pipeline segment runs a read-only program and at
// least one segment names a file to read. Conservative by design — anything
// unrecognized stays "exec".
func readCommand(command string) bool {
	if len(commandReadPaths(command)) == 0 {
		return false
	}
	cleaned := strings.NewReplacer("2>&1", " ", "2>/dev/null", " ", ">/dev/null", " ", "> /dev/null", " ").Replace(command)
	segments := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '|' || r == ';' || r == '&' || r == '\n'
	})
	for _, segment := range segments {
		if strings.ContainsRune(segment, '>') {
			return false
		}
		fields := strings.Fields(segment)
		for len(fields) > 0 && envAssignRe.MatchString(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		program := strings.ToLower(filepath.Base(fields[0]))
		if program == "sed" && !sedReadsOnly(fields[1:]) {
			return false
		}
		if !readOnlyPrograms[program] && !readPrograms[program] {
			return false
		}
	}
	return true
}

// commandReadPaths returns the file arguments of pager-style segments —
// cat/head/tail/nl and the `sed -n '…p' file` idiom — the shell equivalents
// of opening a file to read it.
func commandReadPaths(command string) []string {
	seen := map[string]bool{}
	var paths []string
	segments := strings.FieldsFunc(command, func(r rune) bool {
		return r == '|' || r == ';' || r == '&' || r == '\n'
	})
	for _, segment := range segments {
		if strings.ContainsRune(segment, '>') {
			continue
		}
		fields := strings.Fields(segment)
		for len(fields) > 0 && envAssignRe.MatchString(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		program := strings.ToLower(filepath.Base(fields[0]))
		if !readPrograms[program] {
			continue
		}
		args := fields[1:]
		scriptArgs := 0
		if program == "sed" {
			// only the read idiom: sed -n '…p' file — anything else can rewrite
			if !sedReadsOnly(args) {
				continue
			}
			scriptArgs = 1
		}
		expectValue := false
		for _, arg := range args {
			if expectValue {
				expectValue = false
				continue
			}
			if strings.HasPrefix(arg, "-") {
				expectValue = flagTakesValue(program, arg)
				continue
			}
			if scriptArgs > 0 {
				scriptArgs--
				continue
			}
			// globs, redirections, and substitutions are not literal file paths
			if strings.ContainsAny(arg, "<>*?$`") {
				continue
			}
			path, ok := cleanExtractedPath(arg, true)
			if !ok || seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func sedReadsOnly(args []string) bool {
	hasN := false
	for _, arg := range args {
		if arg == "-n" {
			hasN = true
		}
		if strings.HasPrefix(arg, "-i") {
			return false
		}
	}
	return hasN
}

func flagTakesValue(program, flag string) bool {
	switch program {
	case "head", "tail":
		return flag == "-n" || flag == "-c"
	case "sed":
		return flag == "-e" || flag == "-f"
	}
	return false
}

func verifyCommand(command string) bool {
	c := strings.ToLower(command)
	patterns := []string{"go test", "go vet", "npm test", "npm run build", "pnpm test", "pnpm build", "pytest", "make test", "cargo test", "swift test"}
	for _, pattern := range patterns {
		if strings.Contains(c, pattern) {
			return true
		}
	}
	return false
}

func SummarizeTool(tool string, input map[string]any, targets []model.Target, outside []model.OutsideTouch, isError bool) string {
	verb := tool
	if desc, ok := input["description"].(string); ok && desc != "" {
		verb = desc
	}
	if command := firstString(input, "command", "cmd"); command != "" {
		commandRunes := []rune(command)
		verb = command
		if len(commandRunes) > 96 {
			verb = string(commandRunes[:93]) + "..."
		}
	}
	status := ""
	if isError {
		status = " error"
	}
	return fmt.Sprintf("%s -> %d targets, %d outside%s", verb, len(targets), len(outside), status)
}
