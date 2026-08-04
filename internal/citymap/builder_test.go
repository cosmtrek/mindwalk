package citymap

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestBuildIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "README.md", "# Demo\n")
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	first, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Repo.GeneratedAt = ""
	second.Repo.GeneratedAt = ""
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("citymap is not deterministic\nfirst=%s\nsecond=%s", a, b)
	}
	if len(first.Files) != 2 {
		t.Fatalf("files = %d", len(first.Files))
	}
	if first.Files[0].Rect.W <= 0 || first.Files[0].Rect.D <= 0 {
		t.Fatalf("empty rect: %#v", first.Files[0].Rect)
	}
}

func TestBuildIncludesUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "tracked.go", "package main\n")
	runGit(t, root, "init")
	runGit(t, root, "add", "tracked.go")
	writeFile(t, root, "new.go", "package main\nfunc New() {}\n")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	if !paths["tracked.go"] || !paths["new.go"] {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestBuildIsDeterministicAcrossNestedDirectories(t *testing.T) {
	root := t.TempDir()
	for d := 0; d < 12; d++ {
		for f := 0; f < 8; f++ {
			rel := filepath.Join("pkg", string(rune('a'+d)), "sub", string(rune('a'+f))+".go")
			writeFile(t, root, rel, strings.Repeat("package pkg\n", 1+d*f+f))
		}
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	var first []byte
	for i := 0; i < 20; i++ {
		city, err := (Builder{}).Build(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		city.Repo.GeneratedAt = ""
		data, _ := json.Marshal(city)
		if i == 0 {
			first = data
			continue
		}
		if string(data) != string(first) {
			t.Fatalf("citymap changed on run %d\nfirst=%s\ncurrent=%s", i, first, data)
		}
	}
}

func TestBuildSkipsWeakMissingTargetsButKeepsStrongGhosts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "tracked.go", "package main\n")
	runGit(t, root, "init")
	runGit(t, root, "add", "tracked.go")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{
			{Path: "missing-weak.go", Touch: "hit", Weak: true},
			{Path: "missing-strong.go", Touch: "edit"},
		},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]model.CityFile{}
	for _, file := range city.Files {
		files[file.Path] = file
	}
	if _, ok := files["missing-weak.go"]; ok {
		t.Fatalf("weak missing target became a city file: %#v", files["missing-weak.go"])
	}
	if file, ok := files["missing-strong.go"]; !ok || !file.Ghost {
		t.Fatalf("strong missing target did not become ghost: %#v", file)
	}
}

func TestSquarifiedLayoutAvoidsExtremeAspectRatios(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 80; i++ {
		writeFile(t, root, filepath.Join("pkg", "file"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go"), "package pkg\nfunc X() {}\n")
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	maxRatio := 0.0
	for _, file := range city.Files {
		if file.Rect.W <= 0 || file.Rect.D <= 0 {
			t.Fatalf("empty rect for %s: %#v", file.Path, file.Rect)
		}
		ratio := math.Max(file.Rect.W/file.Rect.D, file.Rect.D/file.Rect.W)
		maxRatio = math.Max(maxRatio, ratio)
	}
	if maxRatio > 25 {
		t.Fatalf("max aspect ratio = %f", maxRatio)
	}
}

func TestLargeTextFileCountsLines(t *testing.T) {
	root := t.TempDir()
	const lines = 100000 // ~1.3MB, under maxLineCountBytes
	writeFile(t, root, "big.go", strings.Repeat("package main\n", lines))

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 {
		t.Fatalf("files = %#v", city.Files)
	}
	if city.Files[0].Lines != lines {
		t.Fatalf("lines = %d, want %d", city.Files[0].Lines, lines)
	}
}

func TestOversizedFileSkipsLineCounting(t *testing.T) {
	root := t.TempDir()
	const lines = 700000 // ~9MB, over maxLineCountBytes
	writeFile(t, root, "huge.go", strings.Repeat("package main\n", lines))

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 {
		t.Fatalf("files = %#v", city.Files)
	}
	if city.Files[0].Lines != 0 {
		t.Fatalf("lines = %d, want 0 (skipped)", city.Files[0].Lines)
	}
	if city.Files[0].Rect.W <= 0 || city.Files[0].Rect.D <= 0 {
		t.Fatalf("byte-weight fallback produced empty rect: %#v", city.Files[0].Rect)
	}
}

func withMapFileCap(t *testing.T, cap int) {
	t.Helper()
	prev := maxMapFiles
	maxMapFiles = cap
	t.Cleanup(func() { maxMapFiles = prev })
}

func TestBuildCapsFilesLevelByLevelPreferringNonDot(t *testing.T) {
	withMapFileCap(t, 6)
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n") // marker → project mode
	writeFile(t, root, "a.go", "package main\n")
	writeFile(t, root, ".hidden.go", "package main\n")
	for _, name := range []string{"c1.go", "c2.go", "c3.go", "c4.go"} {
		writeFile(t, root, "sub/"+name, "package sub\n")
	}
	writeFile(t, root, "sub/deep/d1.go", "package deep\n")
	writeFile(t, root, "sub/deep/d2.go", "package deep\n")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !city.Repo.Truncated {
		t.Fatal("expected truncated map")
	}
	got := make([]string, 0, len(city.Files))
	for _, file := range city.Files {
		got = append(got, file.Path)
	}
	want := []string{".hidden.go", "a.go", "go.mod", "sub/c1.go", "sub/c2.go", "sub/c3.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestWorkspaceModeMapsOnlyProjectSubtrees(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "proj/go.mod", "module proj\n")
	writeFile(t, root, "proj/main.go", "package main\n")
	writeFile(t, root, "proj/src/x.go", "package src\n")
	writeFile(t, root, "clutter/big.txt", "x\n")
	writeFile(t, root, ".cache/junk.go", "package junk\n")
	writeFile(t, root, "loose.txt", "note\n")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	for _, want := range []string{"proj/go.mod", "proj/main.go", "proj/src/x.go", "loose.txt"} {
		if !paths[want] {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
	for _, banned := range []string{"clutter/big.txt", ".cache/junk.go"} {
		if paths[banned] {
			t.Fatalf("workspace scan leaked %s", banned)
		}
	}
	if city.Repo.Truncated {
		t.Fatal("policy pruning must not mark the map truncated")
	}
}

func TestWorkspaceModeTraceSeedsNonProjectDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "proj/go.mod", "module proj\n")
	writeFile(t, root, "notes/a.md", "# a\n")
	writeFile(t, root, "notes/b.md", "# b\n")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "notes/a.md", Touch: "read"}},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	if !paths["notes/a.md"] || !paths["notes/b.md"] {
		t.Fatalf("touched dir not seeded: %v", paths)
	}

	city, err = (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range city.Files {
		if strings.HasPrefix(file.Path, "notes/") {
			t.Fatalf("untouched non-project dir mapped: %s", file.Path)
		}
	}
}

func TestWorkspaceModeGatesProtectedDirsUntilTouched(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "proj/go.mod", "module proj\n")
	writeFile(t, root, "Desktop/demo/go.mod", "module demo\n")
	writeFile(t, root, "Desktop/demo/main.go", "package main\n")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range city.Files {
		if strings.HasPrefix(file.Path, "Desktop/") {
			t.Fatalf("protected dir entered without a touch: %s", file.Path)
		}
	}

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "Desktop/demo/main.go", Touch: "edit"}},
	}}}
	city, err = (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range city.Files {
		if file.Path == "Desktop/demo/main.go" && !file.Ghost {
			found = true
		}
	}
	if !found {
		t.Fatalf("touched protected dir not mapped: %#v", city.Files)
	}
}

func TestGitListOverflowTruncates(t *testing.T) {
	prev := maxGitListPaths
	maxGitListPaths = 3
	t.Cleanup(func() { maxGitListPaths = prev })

	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		writeFile(t, root, name, "package main\n")
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 3 {
		t.Fatalf("files = %d, want 3", len(city.Files))
	}
	if !city.Repo.Truncated {
		t.Fatal("expected truncated map")
	}
}

func TestDeletedTrackedFilesDoNotConsumeBudget(t *testing.T) {
	withMapFileCap(t, 3)
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go", "z-live.go"} {
		writeFile(t, root, name, "package main\n")
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 || city.Files[0].Path != "z-live.go" {
		t.Fatalf("files = %#v, want just z-live.go", city.Files)
	}
	if city.Repo.Truncated {
		t.Fatal("map holds everything that exists — must not be truncated")
	}
}

func TestTouchedFileBeyondWalkHorizonStaysReal(t *testing.T) {
	withMapFileCap(t, 2)
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "a.go", "package main\n")
	writeFile(t, root, "sub/important.go", "package sub\n")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "sub/important.go", Touch: "edit"}},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 2 {
		t.Fatalf("files = %#v, want 2 (budget)", city.Files)
	}
	var important *model.CityFile
	for i := range city.Files {
		if city.Files[i].Path == "sub/important.go" {
			important = &city.Files[i]
		}
	}
	if important == nil || important.Ghost || important.Lines == 0 || important.Bytes == 0 {
		t.Fatalf("touched file beyond walk horizon mishandled: %#v", important)
	}
	if !city.Repo.Truncated {
		t.Fatal("expected truncated map")
	}
}

func TestGhostsShareTheMapBudget(t *testing.T) {
	withMapFileCap(t, 2)
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "a.go", "package main\n")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{
			{Path: "missing1.go", Touch: "edit"},
			{Path: "missing2.go", Touch: "edit"},
			{Path: "missing3.go", Touch: "edit"},
		},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 2 {
		t.Fatalf("files = %d, want the cap of 2 (ghosts included)", len(city.Files))
	}
	for _, file := range city.Files {
		if !file.Ghost {
			t.Fatalf("trace-derived entries outrank untouched files, got %#v", file)
		}
	}
	if !city.Repo.Truncated {
		t.Fatal("dropped targets must mark the map truncated")
	}
}

func TestRealTouchedOutranksGhosts(t *testing.T) {
	withMapFileCap(t, 1)
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "real.go", "package main\n")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{
			{Path: "missing.go", Touch: "edit"},
			{Path: "real.go", Touch: "edit"},
		},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 || city.Files[0].Path != "real.go" || city.Files[0].Ghost {
		t.Fatalf("existing touched file must outrank ghosts, got %#v", city.Files)
	}
	if !city.Repo.Truncated {
		t.Fatal("dropped ghost must mark the map truncated")
	}
}

func TestWeakMissDoesNotMaskLaterStrongTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "tracked.go", "package main\n")
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{
			{Path: "x.go", Touch: "hit", Weak: true},
			{Path: "x.go", Touch: "edit"},
		},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range city.Files {
		if file.Path == "x.go" && file.Ghost {
			found = true
		}
	}
	if !found {
		t.Fatalf("strong miss must become ghost despite an earlier weak miss: %#v", city.Files)
	}
}

func TestNoFalseTruncatedWhenEverythingFits(t *testing.T) {
	withMapFileCap(t, 1)
	root := t.TempDir()
	writeFile(t, root, "only.go", "package main\n")
	runGit(t, root, "init")
	runGit(t, root, "add", ".")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "only.go", Touch: "edit"}},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 || city.Files[0].Path != "only.go" {
		t.Fatalf("files = %#v", city.Files)
	}
	if city.Repo.Truncated {
		t.Fatal("nothing was dropped — truncated must stay false")
	}
}

func TestNonRegularEntriesArePolicySkipsNotTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "a.go", "package main\n")
	writeFile(t, root, "sub/real.go", "package sub\n")
	// a symlink resolving to a directory is collected by the walk but can
	// never become a building
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range city.Files {
		if file.Path == "link" {
			t.Fatalf("symlinked directory became a building: %#v", file)
		}
	}
	if city.Repo.Truncated {
		t.Fatal("policy-skipped non-regular entries must not mark the map partial")
	}

	// same rule on the trace path: not gone, so no ghost — and no badge
	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "link", Touch: "edit"}},
	}}}
	city, err = (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range city.Files {
		if file.Path == "link" {
			t.Fatalf("touched non-regular entry entered the map: %#v", file)
		}
	}
	if city.Repo.Truncated {
		t.Fatal("touched non-regular entries are policy skips too")
	}
}

func TestMarkerlessWorkspaceMapsOnlyLooseFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "loose.txt", "note\n")
	writeFile(t, root, "clutter/nested.txt", "x\n")

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	if !paths["loose.txt"] {
		t.Fatalf("root loose file missing: %v", paths)
	}
	if paths["clutter/nested.txt"] {
		t.Fatal("markerless workspace must not map nested clutter")
	}
	if city.Repo.Truncated {
		t.Fatal("policy pruning must not mark the map truncated")
	}

	// a touched file still pulls its directory in via the seed mechanism
	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "clutter/nested.txt", Touch: "read"}},
	}}}
	city, err = (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range city.Files {
		if file.Path == "clutter/nested.txt" && !file.Ghost {
			found = true
		}
	}
	if !found {
		t.Fatalf("touched clutter must be seeded into the map: %#v", city.Files)
	}
}

func TestProbeReadFailureMarksTruncated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — permissions are not enforced")
	}
	root := t.TempDir()
	writeFile(t, root, "proj/go.mod", "module proj\n")
	writeFile(t, root, "proj/main.go", "package main\n")
	writeFile(t, root, "locked/hidden/go.mod", "module hidden\n")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	if !paths["proj/main.go"] {
		t.Fatalf("visible project missing: %v", paths)
	}
	if !city.Repo.Truncated {
		t.Fatal("an unreadable dir may hide projects — the map must be partial")
	}
}

func TestStaleTraceSeedsNeitherStallNorTruncate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "proj/go.mod", "module proj\n")
	writeFile(t, root, "loose.txt", "note\n")

	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{
			{Path: "gone/sub/file.go", Touch: "edit"}, // parent dir never existed
			{Path: "loose.txt/x.go", Touch: "edit"},   // parent is a file, not a dir
		},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	ghosts := map[string]bool{}
	for _, file := range city.Files {
		if file.Ghost {
			ghosts[file.Path] = true
		}
	}
	if !ghosts["gone/sub/file.go"] || !ghosts["loose.txt/x.go"] {
		t.Fatalf("confirmed-gone strong targets must ghost: %v", ghosts)
	}
	if city.Repo.Truncated {
		t.Fatal("stale seeds lose nothing — truncated must stay false")
	}
}

// TestAllFilesystemAccessIsBounded pins the invariant that ended four review
// rounds of hang bugs: every filesystem or subprocess call in builder.go
// lives inside a bounded helper. New code must route through
// readDirBounded, inspectFileBounded (inspectFile and its readers), or the
// git helpers — or extend this list consciously.
func TestAllFilesystemAccessIsBounded(t *testing.T) {
	src, err := os.ReadFile("builder.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string][]string{
		"os.ReadDir(":          {"readDirBounded("},
		"os.Stat(":             {"inspectFile("},
		"os.Open(":             {"isBinaryLike(", "countLines("},
		"exec.CommandContext(": {"gitListFiles(", "gitOutput("},
	}
	banned := []string{"exec.Command(", "filepath.Walk(", "filepath.WalkDir(", "os.Lstat(", "os.ReadFile(", "os.Create(", "ioutil."}
	current := ""
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "func ") {
			current = line
		}
		for pattern, owners := range allowed {
			if !strings.Contains(line, pattern) {
				continue
			}
			ok := false
			for _, owner := range owners {
				if strings.Contains(current, owner) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("builder.go:%d: %s outside its bounded owners %v (in %q)", i+1, pattern, owners, current)
			}
		}
		for _, pattern := range banned {
			if strings.Contains(line, pattern) {
				t.Errorf("builder.go:%d: banned unbounded call %s — route through the bounded helpers", i+1, pattern)
			}
		}
	}
}

func TestGitCallsAreBounded(t *testing.T) {
	prev := gitTimeout
	gitTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gitTimeout = prev })

	fake := t.TempDir()
	script := "#!/bin/sh\ncase \"$3\" in\nls-files) exit 1 ;;\nesac\nexec sleep 5\n"
	if err := os.WriteFile(filepath.Join(fake, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "a.go", "package main\n")

	start := time.Now()
	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("build took %v against a hanging git", elapsed)
	}
	if city.Repo.Commit != "" || city.Repo.Dirty {
		t.Fatalf("repo state must degrade to empty on timeout: %#v", city.Repo)
	}
}

func TestWalkSkipsUnreadableDirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — permissions are not enforced")
	}
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module demo\n")
	writeFile(t, root, "ok/a.go", "package ok\n")
	writeFile(t, root, "locked/b.go", "package locked\n")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range city.Files {
		paths[file.Path] = true
	}
	if !paths["ok/a.go"] {
		t.Fatalf("readable file missing: %v", paths)
	}
	if paths["locked/b.go"] {
		t.Fatal("unreadable dir contents leaked into the map")
	}
	if !city.Repo.Truncated {
		t.Fatal("losing a subtree to a read failure must mark the map partial")
	}
}

func TestInspectFailureDoesNotGhost(t *testing.T) {
	prev := inspectTimeout
	inspectTimeout = time.Nanosecond
	t.Cleanup(func() { inspectTimeout = prev })

	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n")
	writeFile(t, root, "real.go", "package main\n")
	trace := &model.Trace{Events: []model.Event{{
		Targets: []model.Target{{Path: "real.go", Touch: "edit"}},
	}}}
	city, err := (Builder{}).Build(root, trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range city.Files {
		if file.Ghost {
			t.Fatalf("unproven absence became ghost: %#v", file)
		}
	}
	if !city.Repo.Truncated {
		t.Fatal("inspect failures lose content — the map must be partial")
	}
}

func TestDeletedTailCandidateDoesNotMarkTruncated(t *testing.T) {
	withMapFileCap(t, 1)
	root := t.TempDir()
	writeFile(t, root, "a-live.go", "package main\n")
	writeFile(t, root, "z-deleted.go", "package main\n")
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	if err := os.Remove(filepath.Join(root, "z-deleted.go")); err != nil {
		t.Fatal(err)
	}

	city, err := (Builder{}).Build(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 || city.Files[0].Path != "a-live.go" {
		t.Fatalf("files = %#v", city.Files)
	}
	if city.Repo.Truncated {
		t.Fatal("every mappable file is on the map — truncated must stay false")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
