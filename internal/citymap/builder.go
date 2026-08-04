package citymap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

type Builder struct{}

// Scan limits. Package-level vars so tests can shrink them; production code
// treats them as constants.
var (
	// maxMapFiles caps how many files a citymap holds — past this the
	// browser scene degrades, so the tree is expanded level by level and
	// cut at the budget.
	maxMapFiles = 10000
	// maxGitListPaths bounds the streamed git ls-files read; a git repo at
	// $HOME (dotfiles setups) lists every untracked file beneath it.
	maxGitListPaths = 100000
	// maxLineCountBytes caps line counting; larger files fall back to
	// byte-based weight instead of being read end to end.
	maxLineCountBytes int64 = 2 << 20
	// readDirTimeout bounds a single directory read. A healthy filesystem
	// answers in microseconds; only TCC-gated folders, dead mounts, and
	// cloud placeholders hit this, and losing their subtree beats hanging.
	readDirTimeout = 5 * time.Second
	// inspectTimeout bounds a single file inspection (stat + line count)
	// the same way — cloud placeholder files materialize on open.
	inspectTimeout = 5 * time.Second
	// gitTimeout bounds every git subprocess — ls-files and the repo-state
	// probes alike; git status walks the whole tree and hangs where walks
	// hang.
	gitTimeout = 30 * time.Second
)

const (
	// probeDepth is how deep workspace mode looks for project roots.
	probeDepth = 4
	// maxScanRoots bounds how many project roots a workspace walk fans out to.
	maxScanRoots = 256
	// maxWalkDepth is a safety rail against pathologically deep trees.
	maxWalkDepth = 32
)

// junkDirNames are never entered during walks: generated trees that would
// drown the map (node_modules) and, at $HOME scale, the macOS Library.
var junkDirNames = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
	"target":       true,
	"__pycache__":  true,
	"Library":      true,
}

// junkBundleSuffixes are macOS package directories walks never enter —
// opaque app/media bundles, some of them TCC-protected.
var junkBundleSuffixes = []string{".app", ".photoslibrary", ".musiclibrary", ".tvlibrary", ".fcpbundle", ".framework"}

// protectedDirNames are the macOS folders under TCC consent — reading them
// (or, for the media homes, their library subfolders) can block on a
// permission decision that a background process never gets to answer.
// Workspace mode enters them only when the trace touched something inside —
// a touch means the grant almost certainly already exists.
var protectedDirNames = map[string]bool{
	"Desktop":   true,
	"Documents": true,
	"Downloads": true,
	"Movies":    true,
	"Music":     true,
	"Pictures":  true,
}

// projectMarkerNames identify a directory as a project root during
// workspace-mode probing.
var projectMarkerNames = map[string]bool{
	".git":             true,
	"go.mod":           true,
	"package.json":     true,
	"Cargo.toml":       true,
	"pyproject.toml":   true,
	"setup.py":         true,
	"requirements.txt": true,
	"Gemfile":          true,
	"pom.xml":          true,
	"build.gradle":     true,
	"build.gradle.kts": true,
	"CMakeLists.txt":   true,
	"Makefile":         true,
	"mix.exs":          true,
	"composer.json":    true,
	"Package.swift":    true,
	"deno.json":        true,
	"pubspec.yaml":     true,
}

func (Builder) Build(repoRoot string, trace *model.Trace) (*model.CityMap, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	touched := touchedPaths(trace)
	candidates, truncated, err := listFiles(root, touched)
	if err != nil {
		return nil, err
	}

	// The map budget is spent on entries that earn their slot, in three
	// tiers: trace-touched files that exist come first (a budget cut must
	// never drop them or mislabel them ghost), then ghosts for touched
	// files that are genuinely gone, then everything else the scan found.
	// Inspection failures (deleted-but-tracked paths, unreadable entries)
	// consume no budget, and nothing — ghosts included — may push the map
	// past maxMapFiles.
	seen := map[string]bool{}
	cityFiles := make([]model.CityFile, 0, min(len(candidates), maxMapFiles))
	if trace != nil {
		// Aggregate targets before seating anything: dedup by canonical
		// path in first-appearance order (deterministic), with strength
		// upgraded if any occurrence is strong — a weak miss must not mask
		// a later strong touch of the same path. Seating then runs in two
		// passes, existing files before any ghost, so the tier order holds
		// even when the budget runs out mid-trace.
		type touchedTarget struct {
			raw    string
			rel    string
			strong bool
		}
		var targets []touchedTarget
		index := map[string]int{}
		for _, event := range trace.Events {
			for _, target := range event.Targets {
				raw := target.Path
				if raw == "" {
					continue
				}
				rel := cleanRel(raw)
				key := raw
				if rel != "" {
					key = rel
				}
				if i, ok := index[key]; ok {
					if !target.Weak {
						targets[i].strong = true
					}
					continue
				}
				index[key] = len(targets)
				targets = append(targets, touchedTarget{raw: raw, rel: rel, strong: !target.Weak})
			}
		}
		ghosts := make([]touchedTarget, 0, len(targets))
		for _, tt := range targets {
			if tt.rel != "" {
				meta, err := inspectFileBounded(root, tt.rel)
				if err == nil {
					seen[tt.rel] = true
					seen[tt.raw] = true
					if len(cityFiles) >= maxMapFiles {
						truncated = true
						continue
					}
					cityFiles = append(cityFiles, meta)
					continue
				}
				if errors.Is(err, errNotRegular) {
					// exists but is not city material — policy skip
					continue
				}
				if !benignReadError(err) {
					// timeout, permission — the file is not proven gone,
					// so a ghost would lie; the map is just missing
					// something it wanted to show.
					truncated = true
					continue
				}
			}
			if tt.strong {
				ghosts = append(ghosts, tt)
			}
		}
		for _, tt := range ghosts {
			if len(cityFiles) >= maxMapFiles {
				truncated = true
				break
			}
			seen[tt.raw] = true
			if tt.rel != "" {
				seen[tt.rel] = true
			}
			cityFiles = append(cityFiles, model.CityFile{
				Path:  tt.raw,
				Dir:   filepath.ToSlash(filepath.Dir(tt.raw)),
				Lang:  langForPath(tt.raw),
				Ghost: true,
			})
		}
	}
	for _, rel := range candidates {
		if seen[rel] {
			continue
		}
		meta, err := inspectFileBounded(root, rel)
		if err != nil {
			// vanished files and policy-skipped non-regular entries never
			// truncate the map; anything else — timeout, permission — is
			// real content the map lost
			if !benignReadError(err) && !errors.Is(err, errNotRegular) {
				truncated = true
			}
			continue
		}
		if len(cityFiles) >= maxMapFiles {
			// a genuinely mappable new file cannot fit
			truncated = true
			break
		}
		seen[rel] = true
		cityFiles = append(cityFiles, meta)
	}

	sort.Slice(cityFiles, func(i, j int) bool {
		return cityFiles[i].Path < cityFiles[j].Path
	})
	for i := range cityFiles {
		cityFiles[i].ID = i
	}

	rootNode := buildTree(cityFiles)
	dirs := make([]model.CityDir, 0)
	layoutNode(rootNode, model.Rect{X: 0, Z: 0, W: 120, D: 120}, &cityFiles, &dirs)

	commit, dirty := repoState(root)
	return &model.CityMap{
		Version: 1,
		Repo: model.RepoMeta{
			Root:        root,
			Commit:      commit,
			Dirty:       dirty,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Truncated:   truncated,
		},
		Files: cityFiles,
		Dirs:  dirs,
		Layout: model.LayoutMeta{
			Algorithm: "squarified-treemap-v1",
			Weight:    "sqrt(max(lines, bytes/4096, 16))",
		},
	}, nil
}

// touchedPaths collects every trace target path. The set steers scanning: a
// touched file is the last to be cut, and a touched directory is worth
// entering even where the scan would otherwise not look.
func touchedPaths(trace *model.Trace) map[string]bool {
	if trace == nil {
		return nil
	}
	touched := map[string]bool{}
	for _, event := range trace.Events {
		for _, target := range event.Targets {
			if target.Path != "" {
				touched[target.Path] = true
			}
		}
	}
	return touched
}

// listFiles enumerates candidate files for the citymap, bounded so a huge
// root (a $HOME session) cannot stall the build. Three modes, picked from
// the shape of root:
//
//   - repo: root is a git repo — stream git ls-files
//   - project: root has a project marker but no git — walk the whole tree
//   - workspace: neither — walk only subtrees that look like projects
//
// The result is in budget-fill order but deliberately not capped: the
// maxMapFiles budget is applied by Build, which charges it only for files
// that actually exist. truncated reports scan-budget cuts (git overflow,
// walk level stop, root cap) — Build adds its own when the fill overflows.
func listFiles(root string, touched map[string]bool) ([]string, bool, error) {
	files, overflow, err := gitListFiles(root)
	if err == nil && len(files) > 0 {
		return orderCandidates(files), overflow, nil
	}

	workspace := !hasProjectMarker(root)
	var scanRoots []string
	probeTruncated := false
	if workspace {
		scanRoots, probeTruncated = probeProjectRoots(root, touched)
	}
	files, walkTruncated := walkFiles(root, scanRoots, workspace)
	if len(files) == 0 {
		return nil, false, errors.New("no files found")
	}
	return orderCandidates(files), probeTruncated || walkTruncated, nil
}

// gitListFiles streams git ls-files so a repo spanning a huge tree (a
// dotfiles repo at $HOME lists every untracked file beneath it via -o)
// cannot buffer gigabytes; reading stops at maxGitListPaths.
func gitListFiles(root string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(splitNul)
	var files []string
	overflow := false
	for scanner.Scan() {
		if len(files) >= maxGitListPaths {
			overflow = true
			break
		}
		if p := scanner.Text(); p != "" {
			files = append(files, filepath.ToSlash(p))
		}
	}
	if overflow {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return files, true, nil
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, false, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, false, err
	}
	return files, false, nil
}

func splitNul(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func hasProjectMarker(root string) bool {
	entries, err := readDirBounded(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if projectMarkerNames[entry.Name()] {
			return true
		}
	}
	return false
}

type walkEntry struct {
	rel   string
	depth int
}

// skipWalkDir reports whether a walk should stay out of a directory:
// dot-prefixed trees, generated junk, and macOS bundles.
func skipWalkDir(name string) bool {
	if strings.HasPrefix(name, ".") || junkDirNames[name] {
		return true
	}
	lower := strings.ToLower(name)
	for _, suffix := range junkBundleSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// probeProjectRoots shallow-scans a workspace root (a directory that is
// neither a git repo nor a project itself, $HOME being the canonical case)
// for subdirectories that look like project roots. It reads at most
// probeDepth levels, never enters hidden or junk directories, and enters
// the TCC-protected macOS folders only when the trace touched them. The
// parent directory of every touched file is seeded in as a root too —
// unverified, since the walk's bounded read validates it and a stat here
// would be an unbounded syscall. The second return reports partial
// discovery: a root cap or a non-benign read failure.
func probeProjectRoots(root string, touched map[string]bool) ([]string, bool) {
	rootsSet := map[string]bool{}
	for p := range touched {
		rel := cleanRel(p)
		if rel == "" || !strings.Contains(rel, "/") {
			continue
		}
		rootsSet[path.Dir(rel)] = true
	}

	partial := false
	touchedTop := touchedTopDirs(touched)
	queue := []walkEntry{{}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		entries, err := readDirBounded(filepath.Join(root, filepath.FromSlash(cur.rel)))
		if err != nil {
			if !benignReadError(err) {
				// an unreadable directory may hide whole projects
				partial = true
			}
			continue
		}
		if cur.rel != "" {
			marker := false
			for _, entry := range entries {
				if projectMarkerNames[entry.Name()] {
					marker = true
					break
				}
			}
			if marker {
				// a project root is scanned by the walk, not the probe
				rootsSet[cur.rel] = true
				continue
			}
		}
		if cur.depth >= probeDepth {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if skipWalkDir(name) {
				continue
			}
			if cur.rel == "" && protectedDirNames[name] && !touchedTop[name] {
				continue
			}
			queue = append(queue, walkEntry{rel: joinRel(cur.rel, name), depth: cur.depth + 1})
		}
	}

	roots := make([]string, 0, len(rootsSet))
	for rel := range rootsSet {
		roots = append(roots, rel)
	}
	sort.Strings(roots)
	kept := make([]string, 0, len(roots))
	isRoot := map[string]bool{}
	for _, rel := range roots {
		if underAny(rel, isRoot) {
			continue
		}
		isRoot[rel] = true
		kept = append(kept, rel)
	}
	if len(kept) > maxScanRoots {
		kept = kept[:maxScanRoots]
		partial = true
	}
	return kept, partial
}

// underAny reports whether any ancestor directory of rel is in set.
func underAny(rel string, set map[string]bool) bool {
	for i := strings.IndexByte(rel, '/'); i > 0; {
		if set[rel[:i]] {
			return true
		}
		next := strings.IndexByte(rel[i+1:], '/')
		if next < 0 {
			return false
		}
		i += 1 + next
	}
	return false
}

// walkFiles enumerates files breadth-first, one level at a time, so the
// maxMapFiles budget fills with shallow entries before deep ones and
// enumeration stops at the first level boundary past the budget — deeper
// levels are never even read. In project mode (workspace=false) the whole
// tree is walked. In workspace mode only the root's loose files and the
// discovered scanRoots subtrees are read; no roots means no subtree gets
// walked — that is the only-project-subtrees policy at work, not
// truncation. Unreadable directories are skipped, never fatal.
func walkFiles(root string, scanRoots []string, workspace bool) ([]string, bool) {
	var files []string
	truncated := false

	var queue []walkEntry
	if !workspace {
		queue = []walkEntry{{}}
	} else {
		for _, rel := range scanRoots {
			queue = append(queue, walkEntry{rel: rel, depth: strings.Count(rel, "/") + 1})
		}
		if entries, err := readDirBounded(root); err == nil {
			for _, entry := range entries {
				if mappableFile(entry) {
					files = append(files, entry.Name())
				}
			}
		} else if !benignReadError(err) {
			truncated = true
		}
	}

	for len(queue) > 0 {
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].depth != queue[j].depth {
				return queue[i].depth < queue[j].depth
			}
			return queue[i].rel < queue[j].rel
		})
		depth := queue[0].depth
		wave := 0
		for wave < len(queue) && queue[wave].depth == depth {
			wave++
		}
		if len(files) >= maxMapFiles || depth > maxWalkDepth {
			truncated = true
			break
		}
		level := queue[:wave]
		queue = queue[wave:]
		for _, cur := range level {
			entries, err := readDirBounded(filepath.Join(root, filepath.FromSlash(cur.rel)))
			if err != nil {
				// a subtree the walk chose but could not read is data
				// loss, unlike the policy skips below; a vanished or
				// never-a-directory seed loses nothing
				if !benignReadError(err) {
					truncated = true
				}
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() {
					if skipWalkDir(name) {
						continue
					}
					// protected-dir gating lives in probeProjectRoots: a
					// workspace walk only ever starts inside scan roots
					queue = append(queue, walkEntry{rel: joinRel(cur.rel, name), depth: depth + 1})
				} else if mappableFile(entry) {
					files = append(files, joinRel(cur.rel, name))
				}
			}
		}
	}
	return files, truncated
}

// orderCandidates puts the candidate list in budget-fill order: plain
// alphabetical when everything fits, otherwise level by level with non-dot
// paths ahead of dot-prefixed ones within a level — the shape the walk
// enumerates in — so the budget cut lands on the deepest, most hidden
// corners.
func orderCandidates(files []string) []string {
	if len(files) <= maxMapFiles {
		sort.Strings(files)
		return files
	}
	type ranked struct {
		path  string
		dot   bool
		depth int
	}
	items := make([]ranked, len(files))
	for i, p := range files {
		items[i] = ranked{
			path:  p,
			depth: strings.Count(p, "/"),
			dot:   strings.HasPrefix(p, ".") || strings.Contains(p, "/."),
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		if a.dot != b.dot {
			return b.dot
		}
		return a.path < b.path
	})
	for i := range items {
		files[i] = items[i].path
	}
	return files
}

// cleanRel normalizes a trace target path into a root-relative slash path,
// or "" when it is absolute or escapes the root.
func cleanRel(p string) string {
	p = path.Clean(filepath.ToSlash(p))
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// errNotRegular marks an entry that exists but is never city material —
// FIFOs, sockets, devices, symlinks resolving to directories. Skipping one
// is policy, not data loss, and it is not proven gone either: no ghost, no
// truncation.
var errNotRegular = errors.New("not a regular file")

// benignReadError reports read failures that lose nothing mappable: the
// path is gone, or was never a directory (a stale trace seed). Everything
// else — timeout, permission, IO — is content the map failed to show and
// must surface as truncated.
func benignReadError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// readDirBounded lists a directory but gives up after readDirTimeout. A
// directory open can block indefinitely — TCC-gated folders wait on a
// consent dialog a background process never sees, dead network mounts and
// cloud placeholders wait on IO — and a hung build is worse than a missing
// subtree. The abandoned goroutine (at most one per hung directory) is the
// price of surviving; the timeout is generous enough that healthy
// filesystems never trip it, keeping the map deterministic in practice.
func readDirBounded(dir string) ([]os.DirEntry, error) {
	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		entries, err := os.ReadDir(dir)
		ch <- result{entries, err}
	}()
	select {
	case r := <-ch:
		return r.entries, r.err
	case <-time.After(readDirTimeout):
		return nil, errors.New("directory read timed out")
	}
}

// mappableFile reports whether a directory entry can go on the map: regular
// files and symlinks only. FIFOs and sockets must never be collected — a
// later open() on a writerless FIFO blocks forever.
func mappableFile(entry os.DirEntry) bool {
	t := entry.Type()
	return t.IsRegular() || t&os.ModeSymlink != 0
}

// touchedTopDirs lists the first path component of every touched file — the
// key that lets a walk enter an otherwise-gated protected directory.
func touchedTopDirs(touched map[string]bool) map[string]bool {
	out := map[string]bool{}
	for p := range touched {
		rel := cleanRel(p)
		if i := strings.Index(rel, "/"); i > 0 {
			out[rel[:i]] = true
		}
	}
	return out
}

// inspectFileBounded runs inspectFile with a deadline — opening a cloud
// placeholder file can block on materialization the way a gated directory
// read blocks on consent. A file that cannot be inspected in time is
// dropped from the map.
func inspectFileBounded(root, rel string) (model.CityFile, error) {
	type result struct {
		meta model.CityFile
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		meta, err := inspectFile(root, rel)
		ch <- result{meta, err}
	}()
	select {
	case r := <-ch:
		return r.meta, r.err
	case <-time.After(inspectTimeout):
		return model.CityFile{}, errors.New("file inspection timed out")
	}
}

func inspectFile(root, rel string) (model.CityFile, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return model.CityFile{}, err
	}
	if !info.Mode().IsRegular() {
		// FIFOs, sockets, devices — opening these can block forever
		return model.CityFile{}, errNotRegular
	}
	lines := 1
	if info.Size() > maxLineCountBytes {
		// too big to read end to end; fileWeight falls back to bytes
		lines = 0
	} else if !isBinaryLike(abs, rel) {
		var err error
		lines, err = countLines(abs)
		if err != nil {
			lines = 0
		}
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	return model.CityFile{
		Path:  filepath.ToSlash(rel),
		Dir:   dir,
		Lines: lines,
		Bytes: info.Size(),
		Lang:  langForPath(rel),
	}, nil
}

func isBinaryLike(abs, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".zip", ".gz", ".tgz", ".mp4", ".mov", ".mp3", ".woff", ".woff2", ".ttf", ".otf":
		return true
	}
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return bytes.Contains(buf[:n], []byte{0})
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	lines := 0
	hasBytes := false
	var last byte
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			hasBytes = true
			last = buf[n-1]
			lines += bytes.Count(buf[:n], []byte{'\n'})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return lines, err
		}
	}
	if hasBytes && last != '\n' {
		lines++
	}
	return lines, nil
}

func langForPath(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	switch ext {
	case "go":
		return "go"
	case "ts", "tsx":
		return "typescript"
	case "js", "jsx":
		return "javascript"
	case "md", "mdx":
		return "markdown"
	case "py":
		return "python"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "css":
		return "css"
	case "html":
		return "html"
	case "":
		return "text"
	default:
		return ext
	}
}

// gitOutput runs one git subcommand against root under the shared deadline —
// no git call may hang the build. WaitDelay forces the pipes shut if a
// grandchild survives the kill and holds them open.
func gitOutput(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

func repoState(root string) (string, bool) {
	commit := ""
	if commitBytes, err := gitOutput(root, "rev-parse", "--short", "HEAD"); err == nil {
		commit = strings.TrimSpace(string(commitBytes))
	}

	statusBytes, err := gitOutput(root, "status", "--porcelain")
	dirty := err == nil && len(bytes.TrimSpace(statusBytes)) > 0
	return commit, dirty
}

type node struct {
	name      string
	path      string
	children  map[string]*node
	files     []int
	weight    float64
	rect      model.Rect
	fileCount int
	lines     int
}

func buildTree(files []model.CityFile) *node {
	root := &node{name: "", children: map[string]*node{}}
	for i, file := range files {
		parts := splitPath(file.Path)
		cur := root
		for _, part := range parts[:len(parts)-1] {
			next := cur.children[part]
			if next == nil {
				nextPath := part
				if cur.path != "" {
					nextPath = cur.path + "/" + part
				}
				next = &node{name: part, path: nextPath, children: map[string]*node{}}
				cur.children[part] = next
			}
			cur = next
		}
		cur.files = append(cur.files, i)
	}
	computeWeight(root, files)
	return root
}

func splitPath(path string) []string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{path}
	}
	return out
}

func computeWeight(n *node, files []model.CityFile) float64 {
	n.weight = 0
	for _, idx := range n.files {
		n.weight += fileWeight(files[idx])
		n.fileCount++
		n.lines += files[idx].Lines
	}
	for _, child := range sortedChildren(n.children) {
		n.weight += computeWeight(child, files)
		n.fileCount += child.fileCount
		n.lines += child.lines
	}
	if n.weight <= 0 {
		n.weight = 1
	}
	return n.weight
}

type layoutItem struct {
	name   string
	kind   string
	idx    int
	node   *node
	weight float64
	area   float64
}

func layoutNode(n *node, rect model.Rect, files *[]model.CityFile, dirs *[]model.CityDir) {
	n.rect = rect
	if n.path != "" {
		*dirs = append(*dirs, model.CityDir{
			Path:      n.path,
			Depth:     len(strings.Split(n.path, "/")),
			Rect:      n.rect,
			FileCount: n.fileCount,
			Lines:     n.lines,
		})
	}

	items := make([]layoutItem, 0, len(n.children)+len(n.files))
	for _, child := range sortedChildren(n.children) {
		items = append(items, layoutItem{name: child.name, kind: "dir", node: child, weight: child.weight})
	}
	for _, idx := range n.files {
		items = append(items, layoutItem{name: (*files)[idx].Path, kind: "file", idx: idx, weight: fileWeight((*files)[idx])})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].weight == items[j].weight {
			return items[i].name < items[j].name
		}
		return items[i].weight > items[j].weight
	})

	total := 0.0
	for _, item := range items {
		total += item.weight
	}
	if total <= 0 {
		return
	}

	scale := rect.W * rect.D / total
	for i := range items {
		items[i].area = items[i].weight * scale
	}
	for _, placed := range squarify(rect, items) {
		childRect := capAspect(inset(placed.rect, 0.08), 40)
		item := placed.item
		if item.kind == "dir" {
			layoutNode(item.node, childRect, files, dirs)
		} else {
			(*files)[item.idx].Rect = childRect
		}
	}
}

func sortedChildren(children map[string]*node) []*node {
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*node, 0, len(names))
	for _, name := range names {
		out = append(out, children[name])
	}
	return out
}

func capAspect(rect model.Rect, maxRatio float64) model.Rect {
	if rect.W <= 0 || rect.D <= 0 || maxRatio <= 1 {
		return rect
	}
	if rect.W/rect.D > maxRatio {
		newW := rect.D * maxRatio
		rect.X += (rect.W - newW) / 2
		rect.W = newW
	} else if rect.D/rect.W > maxRatio {
		newD := rect.W * maxRatio
		rect.Z += (rect.D - newD) / 2
		rect.D = newD
	}
	return rect
}

func fileWeight(file model.CityFile) float64 {
	units := float64(file.Lines)
	if byteUnits := float64(file.Bytes) / 4096.0; byteUnits > units {
		units = byteUnits
	}
	if units < 16 {
		units = 16
	}
	return math.Sqrt(units)
}

type placedItem struct {
	item layoutItem
	rect model.Rect
}

func squarify(rect model.Rect, items []layoutItem) []placedItem {
	remaining := rect
	var placed []placedItem
	var row []layoutItem
	for idx, item := range items {
		if item.area <= 0 {
			continue
		}
		side := math.Min(remaining.W, remaining.D)
		if len(row) < 2 || idx == len(items)-1 || worstAspect(append(row, item), side) <= worstAspect(row, side) {
			row = append(row, item)
			continue
		}
		var rowPlaced []placedItem
		rowPlaced, remaining = layoutRow(remaining, row)
		placed = append(placed, rowPlaced...)
		row = []layoutItem{item}
	}
	if len(row) > 0 {
		rowPlaced, _ := layoutRow(remaining, row)
		placed = append(placed, rowPlaced...)
	}
	return placed
}

func worstAspect(row []layoutItem, side float64) float64 {
	if len(row) == 0 || side <= 0 {
		return math.Inf(1)
	}
	sum := 0.0
	minArea := math.Inf(1)
	maxArea := 0.0
	for _, item := range row {
		sum += item.area
		minArea = math.Min(minArea, item.area)
		maxArea = math.Max(maxArea, item.area)
	}
	if sum <= 0 || minArea <= 0 {
		return math.Inf(1)
	}
	side2 := side * side
	sum2 := sum * sum
	return math.Max(side2*maxArea/sum2, sum2/(side2*minArea))
}

func layoutRow(rect model.Rect, row []layoutItem) ([]placedItem, model.Rect) {
	sum := 0.0
	for _, item := range row {
		sum += item.area
	}
	if sum <= 0 {
		return nil, rect
	}
	placed := make([]placedItem, 0, len(row))
	if rect.W >= rect.D {
		rowD := sum / rect.W
		x := rect.X
		for i, item := range row {
			w := item.area / rowD
			if i == len(row)-1 {
				w = rect.X + rect.W - x
			}
			placed = append(placed, placedItem{item: item, rect: model.Rect{X: x, Z: rect.Z, W: w, D: rowD}})
			x += w
		}
		rect.Z += rowD
		rect.D -= rowD
	} else {
		rowW := sum / rect.D
		z := rect.Z
		for i, item := range row {
			d := item.area / rowW
			if i == len(row)-1 {
				d = rect.Z + rect.D - z
			}
			placed = append(placed, placedItem{item: item, rect: model.Rect{X: rect.X, Z: z, W: rowW, D: d}})
			z += d
		}
		rect.X += rowW
		rect.W -= rowW
	}
	if rect.W < 0 {
		rect.W = 0
	}
	if rect.D < 0 {
		rect.D = 0
	}
	return placed, rect
}

func inset(rect model.Rect, pad float64) model.Rect {
	if rect.W > pad*2 {
		rect.X += pad
		rect.W -= pad * 2
	}
	if rect.D > pad*2 {
		rect.Z += pad
		rect.D -= pad * 2
	}
	return rect
}
