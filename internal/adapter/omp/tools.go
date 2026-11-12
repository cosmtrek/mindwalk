package omp

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
)

// resolveCall reads the omp tools whose arguments or output the shared
// inference cannot: read paths carry line selectors, edit takes a hashline
// or apply_patch document, write may target an internal URI, and grep and
// glob print a heading tree instead of path:line hits. Other tools (bash,
// task, todo, …) return nil and go through the shared inference.
func resolveCall(call adapter.ToolCall, result string) *adapter.ResolvedCall {
	switch call.Name {
	case "read":
		resolved := &adapter.ResolvedCall{Action: "read"}
		raw, _ := call.Input["path"].(string)
		if path, lines, ok := readTarget(raw); ok {
			resolved.Paths = []adapter.PathRef{{Path: path, Touch: "read", Lines: lines}}
		}
		return resolved
	case "write":
		raw, _ := call.Input["path"].(string)
		// a write to xd:// invokes a tool device rather than editing a file
		if strings.HasPrefix(raw, "xd://") {
			return &adapter.ResolvedCall{Action: "other"}
		}
		resolved := &adapter.ResolvedCall{Action: "edit"}
		if path, ok := cleanPath(raw); ok {
			resolved.Paths = []adapter.PathRef{{Path: path, Touch: "edit"}}
		}
		return resolved
	case "edit":
		resolved := &adapter.ResolvedCall{Action: "edit"}
		input, _ := call.Input["input"].(string)
		for _, path := range editPaths(input) {
			resolved.Paths = append(resolved.Paths, adapter.PathRef{Path: path, Touch: "edit"})
		}
		return resolved
	case "grep", "glob":
		resolved := &adapter.ResolvedCall{Action: "search", Paths: treeHits(result, call.Name == "glob")}
		if len(resolved.Paths) == 0 {
			raw, _ := call.Input["path"].(string)
			for _, part := range strings.Split(raw, ";") {
				if strings.ContainsAny(part, "*?[{") {
					continue
				}
				if path, ok := cleanPath(part); ok {
					resolved.Paths = append(resolved.Paths, adapter.PathRef{Path: path, Touch: "hit", Weak: true})
				}
			}
		}
		return resolved
	case "eval":
		return &adapter.ResolvedCall{Action: "exec"}
	}
	return nil
}

var uriSchemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// cleanPath turns an omp path argument into a filesystem path: internal
// URIs (skill://, artifact://, local://, xd://, …) and web URLs name no
// repository file, and a leading ~ expands the way omp's tools expand it.
func cleanPath(raw string) (string, bool) {
	path := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
	if path == "" || uriSchemeRe.MatchString(path) {
		return "", false
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path, true
}

var rangeListRe = regexp.MustCompile(`^(?:\d+(?:-\d*|\+\d+)?|-\d+)(?:,(?:\d+(?:-\d*|\+\d+)?|-\d+))*$`)

// readTarget splits an omp read path into the file and the line ranges its
// selectors name. Selectors chain from the right — `file:5-16,960-973`,
// `file:2-4:raw`, `file:raw:2-4`, `file:img` — and a `?q=` query on JSON
// and SQLite paths is not part of the file name. Open-ended selectors
// (`:50-`, `:50`) mark only their first line; tail selectors (`:-60`) name
// no line the log can place.
func readTarget(raw string) (string, [][2]int, bool) {
	path, ok := cleanPath(raw)
	if !ok {
		return "", nil, false
	}
	if i := strings.IndexByte(path, '?'); i > 0 && strings.Contains(path[i:], "=") {
		path = path[:i]
	}
	var lines [][2]int
	for {
		i := strings.LastIndexByte(path, ':')
		if i <= 0 {
			break
		}
		selector := path[i+1:]
		switch {
		case selector == "raw" || selector == "img" || selector == "conflicts":
		case rangeListRe.MatchString(selector):
			lines = append(lines, selectorLines(selector)...)
		default:
			return path, lines, true
		}
		path = path[:i]
	}
	return path, lines, path != ""
}

func selectorLines(selector string) [][2]int {
	var lines [][2]int
	for _, part := range strings.Split(selector, ",") {
		if strings.HasPrefix(part, "-") {
			continue
		}
		if start, count, ok := strings.Cut(part, "+"); ok {
			first, _ := strconv.Atoi(start)
			n, _ := strconv.Atoi(count)
			if first > 0 && n > 0 {
				lines = append(lines, [2]int{first, first + n - 1})
			}
			continue
		}
		start, end, _ := strings.Cut(part, "-")
		first, _ := strconv.Atoi(start)
		last, err := strconv.Atoi(end)
		if err != nil || last < first {
			last = first
		}
		if first > 0 {
			lines = append(lines, [2]int{first, last})
		}
	}
	return lines
}

var (
	// hashline section header: `[path#TAG]`, the tag being the snapshot the
	// edit was anchored to. The tag is mandatory in omp's edit grammar.
	hashlineHeaderRe = regexp.MustCompile(`^\[([^\]\n]+)#[0-9A-Za-z]+\]\s*$`)
	hashlineMoveRe   = regexp.MustCompile(`^MV\s+(.+?)\s*$`)
)

// editPaths lists the files an omp edit document changes. Hashline
// documents name each file in a `[path#TAG]` section header (body rows
// start with `+`, so a line opening with `[` is always a header) and may
// rename it with `MV dest`; apply_patch envelopes are delegated to the
// shared patch parser.
func editPaths(input string) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(raw string) {
		path, _, ok := readTarget(raw)
		if ok && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	if strings.Contains(input, "*** Begin Patch") {
		for _, path := range adapter.PatchPaths(input) {
			add(path)
		}
		return paths
	}
	for _, line := range strings.Split(input, "\n") {
		if m := hashlineHeaderRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		} else if m := hashlineMoveRe.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
	}
	return paths
}

var (
	treeHeadingRe = regexp.MustCompile(`^(#+) (.+)$`)
	// requiring the snapshot tag keeps bracketed notices such as
	// "[Uneventful result elided]" from reading as file headers
	treeBracketRe = hashlineHeaderRe
	treeLineRe    = regexp.MustCompile(`^([ *])(\d+)[:|]`)
	treeTagRe     = regexp.MustCompile(`#[0-9A-Za-z]+$`)
)

// treeHits reads the heading tree omp's grep and glob print. A heading's
// depth is its nesting: `# dir/` opens a directory, `## sub/` a directory
// inside it, and a heading without a trailing slash is a file (its `#TAG`
// suffix is the read snapshot). A standalone `[path#TAG]` header names one
// file outright. Under a file, `*N:` rows are matches; under a directory,
// glob lists bare file names.
func treeHits(result string, listing bool) []adapter.PathRef {
	var hits []adapter.PathRef
	index := map[string]int{}
	file := ""
	var dirs []string
	addFile := func(path string) {
		file = ""
		path, ok := cleanPath(path)
		if !ok {
			return
		}
		file = path
		if _, seen := index[path]; !seen {
			index[path] = len(hits)
			hits = append(hits, adapter.PathRef{Path: path, Touch: "hit"})
		}
	}
	for _, line := range strings.Split(result, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := treeHeadingRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if level-1 > len(dirs) {
				// a heading deeper than any open directory is not tree
				// structure this reader understands
				dirs, file = nil, ""
				continue
			}
			dirs = dirs[:level-1]
			parent := ""
			if len(dirs) > 0 {
				parent = dirs[len(dirs)-1]
			}
			name := m[2]
			if strings.HasSuffix(name, "/") {
				dirs = append(dirs, joinTree(parent, name))
				file = ""
				continue
			}
			addFile(joinTree(parent, treeTagRe.ReplaceAllString(name, "")))
			continue
		}
		if m := treeBracketRe.FindStringSubmatch(line); m != nil {
			dirs = nil
			addFile(m[1])
			continue
		}
		if m := treeLineRe.FindStringSubmatch(line); m != nil {
			if file != "" && m[1] == "*" {
				n, _ := strconv.Atoi(m[2])
				hit := &hits[index[file]]
				hit.Lines = append(hit.Lines, [2]int{n, n})
			}
			continue
		}
		if listing && len(dirs) > 0 && line != "" && !strings.HasPrefix(line, " ") &&
			!strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "...") && !strings.HasSuffix(line, "/") {
			addFile(joinTree(dirs[len(dirs)-1], line))
			file = ""
		}
	}
	return hits
}

func joinTree(parent, name string) string {
	if parent == "" {
		return strings.TrimSuffix(name, "/")
	}
	return filepath.Join(parent, name)
}
